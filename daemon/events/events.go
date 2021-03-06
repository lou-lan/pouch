package events

import (
	"context"
	"time"

	"github.com/alibaba/pouch/apis/types"

	goevents "github.com/docker/go-events"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Events is pubsub channel for events generated by the engine.
type Events struct {
	broadcaster *goevents.Broadcaster
}

// NewEvents return a new Events instance
func NewEvents() *Events {
	return &Events{
		broadcaster: goevents.NewBroadcaster(),
	}
}

// Publish sends an event. The caller will be considered the initial
// publisher of the event. This means the timestamp will be calculated
// at this point and this method may read from the calling context.
func (e *Events) Publish(ctx context.Context, action string, eventType types.EventType, actor *types.EventsActor) error {
	// ensure actor not nil
	if actor == nil {
		actor = &types.EventsActor{}
	}

	now := time.Now().UTC()
	msg := types.EventsMessage{
		Action:   action,
		Type:     eventType,
		Actor:    actor,
		Time:     now.Unix(),
		TimeNano: now.UnixNano(),
	}

	// compatibility with moby
	switch eventType {
	case types.EventTypeContainer:
		msg.ID = actor.ID
		msg.Status = action
		if actor.Attributes != nil {
			if image, ok := actor.Attributes["image"]; ok {
				msg.From = image
			}
		}
	case types.EventTypeImage:
		msg.ID = actor.ID
		msg.Status = action
	}

	err := e.broadcaster.Write(&msg)
	if err != nil {
		logrus.Errorf("failed to publish event {action: %s, type: %s, id: %s}: %v", msg.Action, msg.Type, msg.ID, err)
	}

	return err
}

// Subscribe to events on the Events. Events are sent through the returned
// channel ch. If an error is encountered, it will be sent on channel errs and
// errs will be closed. To end the subscription, cancel the provided context.
//
// Zero or more filters may be provided as Args. Only events that match
// *any* of the provided filters will be sent on the channel.
func (e *Events) Subscribe(ctx context.Context, ef *Filter) (<-chan *types.EventsMessage, <-chan error) {
	var (
		evch                  = make(chan *types.EventsMessage)
		errq                  = make(chan error, 1)
		channel               = goevents.NewChannel(0)
		queue                 = goevents.NewQueue(channel)
		dst     goevents.Sink = queue
	)

	closeAll := func() {
		close(errq)
		e.broadcaster.Remove(dst)
		queue.Close()
		channel.Close()
	}

	// add filters for event messages
	if ef != nil && ef.filter.Len() > 0 {
		dst = goevents.NewFilter(queue, goevents.MatcherFunc(func(gev goevents.Event) bool {
			// TODO(ziren): maybe we need adaptor here
			msg := gev.(*types.EventsMessage)
			return ef.Match(*msg)
		}))
	}

	e.broadcaster.Add(dst)

	go func() {
		defer closeAll()

		var err error
	loop:
		for {
			select {
			case ev := <-channel.C:
				env, ok := ev.(*types.EventsMessage)
				if !ok {
					err = errors.Errorf("invalid message encountered %#v; please file a bug", ev)
					break
				}

				select {
				case evch <- env:
				case <-ctx.Done():
					break loop
				}
			case <-ctx.Done():
				break loop
			}
		}

		if err == nil {
			if cerr := ctx.Err(); cerr != context.Canceled {
				err = cerr
			}
		}

		errq <- err
	}()

	return evch, errq
}
