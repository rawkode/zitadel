package projection

import (
	"database/sql"
	"testing"
	"time"

	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/eventstore/handler"
	"github.com/zitadel/zitadel/internal/eventstore/repository"
)

func testEvent(
	eventType repository.EventType,
	aggregateType repository.AggregateType,
	data []byte,
) *repository.Event {
	return &repository.Event{
		CreationDate:  time.Now(),
		Type:          eventType,
		AggregateType: aggregateType,
		Data:          data,
		Version:       "v1",
		AggregateID:   "agg-id",
		ResourceOwner: sql.NullString{String: "ro-id", Valid: true},
		InstanceID:    "instance-id",
		ID:            "event-id",
		EditorService: "editor-svc",
		EditorUser:    "editor-user",
	}
}

func baseEvent(*testing.T) eventstore.Event {
	return &eventstore.BaseEvent{}
}

func getEvent(event *repository.Event, mapper func(*repository.Event) (eventstore.Event, error)) func(t *testing.T) eventstore.Event {
	return func(t *testing.T) eventstore.Event {
		e, err := mapper(event)
		if err != nil {
			t.Fatalf("mapper failed: %v", err)
		}
		return e
	}
}

type wantReduce struct {
	aggregateType    eventstore.AggregateType
	sequence         uint64
	previousSequence uint64
	executer         *testExecuter
	err              func(error) bool
}

func assertReduce(t *testing.T, stmt *handler.Statement, err error, projection string, want wantReduce) {
	t.Helper()
	if want.err == nil && err != nil {
		t.Errorf("unexpected error of type %T: %v", err, err)
		return
	}
	if want.err != nil && want.err(err) {
		return
	}
	if stmt.AggregateType != want.aggregateType {
		t.Errorf("wrong aggregate type: want: %q got: %q", want.aggregateType, stmt.AggregateType)
	}

	if stmt.Execute == nil {
		want.executer.Validate(t)
		return
	}
	err = stmt.Execute(want.executer, projection)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	want.executer.Validate(t)
}
