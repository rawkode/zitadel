package command

import (
	"context"
	"math"
	"time"

	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/repository/quota"
)

func (c *Commands) GetDueQuotaNotifications(ctx context.Context, config *quota.AddedEvent, periodStart time.Time, usedAbs uint64) ([]*quota.NotifiedEvent, error) {

	if len(config.Notifications) == 0 {
		return nil, nil
	}

	aggregate := config.Aggregate()
	wm, err := c.getQuotaNotificationsWriteModel(ctx, aggregate, periodStart)
	if err != nil {
		return nil, err
	}

	usedRel := uint64(math.Floor(float64(usedAbs*100) / float64(config.Amount)))

	var dueNotifications []*quota.NotifiedEvent
	for _, notification := range config.Notifications {
		if notification.Percent > usedRel {
			continue
		}

		threshold := notification.Percent
		if notification.Repeat {
			threshold = uint64(math.Min(1, math.Floor(float64(usedRel)/float64(notification.Percent)))) * notification.Percent
		}

		if wm.latestNotifiedThresholds[notification.ID] < threshold {
			dueNotifications = append(
				dueNotifications,
				quota.NewNotifiedEvent(
					ctx,
					&aggregate,
					config.Unit,
					notification.ID,
					notification.CallURL,
					periodStart,
					threshold,
					usedAbs,
				),
			)
		}
	}

	return dueNotifications, nil
}

func (c *Commands) getQuotaNotificationsWriteModel(ctx context.Context, aggregate eventstore.Aggregate, periodStart time.Time) (*quotaNotificationsWriteModel, error) {
	wm := newQuotaNotificationsWriteModel(aggregate.ID, aggregate.InstanceID, aggregate.ResourceOwner, periodStart)
	return wm, c.eventstore.FilterToQueryReducer(ctx, wm)
}
