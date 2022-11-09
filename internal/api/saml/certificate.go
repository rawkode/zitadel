package saml

import (
	"context"
	"fmt"
	"time"

	"github.com/zitadel/logging"
	"github.com/zitadel/saml/pkg/provider/key"
	"gopkg.in/square/go-jose.v2"

	"github.com/zitadel/zitadel/internal/api/authz"
	"github.com/zitadel/zitadel/internal/crypto"
	"github.com/zitadel/zitadel/internal/domain"
	"github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/query"
	"github.com/zitadel/zitadel/internal/repository/instance"
	"github.com/zitadel/zitadel/internal/repository/keypair"
)

const (
	locksTable = "projections.locks"
	signingKey = "signing_key"
	samlUser   = "SAML"

	retryBackoff   = 500 * time.Millisecond
	retryCount     = 3
	lockDuration   = retryCount * retryBackoff * 5
	gracefulPeriod = 10 * time.Minute
)

type CertificateAndKey struct {
	algorithm   jose.SignatureAlgorithm
	id          string
	key         interface{}
	certificate interface{}
}

func (c *CertificateAndKey) SignatureAlgorithm() jose.SignatureAlgorithm {
	return c.algorithm
}

func (c *CertificateAndKey) Key() interface{} {
	return c.key
}

func (c *CertificateAndKey) Certificate() interface{} {
	return c.certificate
}

func (c *CertificateAndKey) ID() string {
	return c.id
}

func (p *Storage) GetCertificateAndKey(ctx context.Context, usage domain.KeyUsage) (certAndKey *key.CertificateAndKey, err error) {
	err = retry(func() error {
		certAndKey, err = p.getCertificateAndKey(ctx, usage)
		if err != nil {
			return err
		}
		if certAndKey == nil {
			return errors.ThrowInternal(err, "SAML-8u01nks", "no certificate found")
		}
		return nil
	})
	return certAndKey, err
}

func (p *Storage) getCertificateAndKey(ctx context.Context, usage domain.KeyUsage) (*key.CertificateAndKey, error) {
	certs, err := p.query.ActiveCertificates(ctx, time.Now().Add(gracefulPeriod), usage)
	if err != nil {
		return nil, err
	}

	if len(certs.Certificates) > 0 {
		return p.certificateToCertificateAndKey(selectCertificate(certs.Certificates))
	}

	var sequence time.Time
	if certs.LatestSequence != nil {
		sequence = certs.LatestSequence.Timestamp
	}

	return nil, p.refreshCertificate(ctx, usage, sequence)
}

func (p *Storage) refreshCertificate(
	ctx context.Context,
	usage domain.KeyUsage,
	sequence time.Time,
) error {
	ok, err := p.ensureIsLatestCertificate(ctx, sequence)
	if err != nil {
		logging.WithError(err).Error("could not ensure latest key")
		return err
	}
	if !ok {
		logging.Warn("view not up to date, retrying later")
		return err
	}
	err = p.lockAndGenerateCertificateAndKey(ctx, usage, sequence)
	logging.OnError(err).Warn("could not create signing key")
	return nil
}

func (p *Storage) ensureIsLatestCertificate(ctx context.Context, currentTimestamp time.Time) (bool, error) {
	maxSequence, err := p.getMaxKeySequence(ctx)
	if err != nil {
		return false, fmt.Errorf("error retrieving new events: %w", err)
	}
	return currentTimestamp.Equal(maxSequence), nil
}

func (p *Storage) lockAndGenerateCertificateAndKey(ctx context.Context, usage domain.KeyUsage, sequence time.Time) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = setSAMLCtx(ctx)

	errs := p.locker.Lock(ctx, lockDuration, authz.GetInstance(ctx).InstanceID())
	err, ok := <-errs
	if err != nil || !ok {
		if errors.IsErrorAlreadyExists(err) {
			return nil
		}
		logging.OnError(err).Warn("initial lock failed")
		return err
	}

	switch usage {
	case domain.KeyUsageSAMLMetadataSigning, domain.KeyUsageSAMLResponseSinging:
		certAndKey, err := p.GetCertificateAndKey(ctx, domain.KeyUsageSAMLCA)
		if err != nil {
			return fmt.Errorf("error while reading ca certificate: %w", err)
		}
		if certAndKey.Key == nil || certAndKey.Certificate == nil {
			return fmt.Errorf("has no ca certificate")
		}

		switch usage {
		case domain.KeyUsageSAMLMetadataSigning:
			return p.command.GenerateSAMLMetadataCertificate(setSAMLCtx(ctx), p.certificateAlgorithm, certAndKey.Key, certAndKey.Certificate)
		case domain.KeyUsageSAMLResponseSinging:
			return p.command.GenerateSAMLResponseCertificate(setSAMLCtx(ctx), p.certificateAlgorithm, certAndKey.Key, certAndKey.Certificate)
		default:
			return fmt.Errorf("unknown usage")
		}
	case domain.KeyUsageSAMLCA:
		return p.command.GenerateSAMLCACertificate(setSAMLCtx(ctx), p.certificateAlgorithm)
	default:
		return fmt.Errorf("unknown certificate usage")
	}
}

func (p *Storage) getMaxKeySequence(ctx context.Context) (time.Time, error) {
	return p.eventstore.LatestCreationDate(ctx,
		eventstore.NewSearchQueryBuilder(eventstore.ColumnsMaxCreationDate).
			ResourceOwner(authz.GetInstance(ctx).InstanceID()).
			AddQuery().
			AggregateTypes(keypair.AggregateType, instance.AggregateType).
			Builder(),
	)
}

func (p *Storage) certificateToCertificateAndKey(certificate query.Certificate) (_ *key.CertificateAndKey, err error) {
	keyData, err := crypto.Decrypt(certificate.Key(), p.encAlg)
	if err != nil {
		return nil, err
	}
	privateKey, err := crypto.BytesToPrivateKey(keyData)
	if err != nil {
		return nil, err
	}

	cert, err := crypto.BytesToCertificate(certificate.Certificate())
	if err != nil {
		return nil, err
	}

	return &key.CertificateAndKey{
		Key:         privateKey,
		Certificate: cert,
	}, nil
}

func selectCertificate(certs []query.Certificate) query.Certificate {
	return certs[len(certs)-1]
}

func setSAMLCtx(ctx context.Context) context.Context {
	return authz.SetCtxData(ctx, authz.CtxData{UserID: samlUser, OrgID: authz.GetInstance(ctx).InstanceID()})
}

func retry(retryable func() error) (err error) {
	for i := 0; i < retryCount; i++ {
		err = retryable()
		if err == nil {
			return nil
		}
		time.Sleep(retryBackoff)
	}
	return err
}
