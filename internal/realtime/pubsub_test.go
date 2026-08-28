package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublisher_Subscriber_DeliversToLocalHub(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	gatewayID := "gw-" + uuid.NewString()
	hub := NewHub(nil)
	userID := uuid.New()
	conn := hub.Register(userID, "eu")

	subscriber := NewSubscriber(client, gatewayID, hub, discardLogger())
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go subscriber.Run(subCtx)
	waitForSubscriber(t, client, gatewayChannel(gatewayID))

	publisher := NewPublisher(client)
	if err := publisher.Push(ctx, gatewayID, []uuid.UUID{userID}, []byte(`{"body":"hi"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}

	assertDelivered(t, "subscriber -> hub", conn)
}

func TestPublisher_Subscriber_MultipleTargetUsersInOnePush(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	gatewayID := "gw-" + uuid.NewString()
	hub := NewHub(nil)
	userA, userB := uuid.New(), uuid.New()
	connA := hub.Register(userA, "eu")
	connB := hub.Register(userB, "eu")

	subscriber := NewSubscriber(client, gatewayID, hub, discardLogger())
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go subscriber.Run(subCtx)
	waitForSubscriber(t, client, gatewayChannel(gatewayID))

	publisher := NewPublisher(client)
	if err := publisher.Push(ctx, gatewayID, []uuid.UUID{userA, userB}, []byte(`{"body":"hi"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}

	assertDelivered(t, "userA", connA)
	assertDelivered(t, "userB", connB)
}

func TestPublisher_Push_UnrelatedGatewayDoesNotReceiveIt(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	targetGateway := "gw-target-" + uuid.NewString()
	otherGateway := "gw-other-" + uuid.NewString()

	targetHub := NewHub(nil)
	otherHub := NewHub(nil)
	userID := uuid.New()
	targetConn := targetHub.Register(userID, "eu")
	// Same userID also has a (stale, unrelated) local entry on otherHub —
	// otherHub must never see this push at all, since it never subscribed
	// to targetGateway's channel.
	otherConn := otherHub.Register(userID, "eu")

	targetSub := NewSubscriber(client, targetGateway, targetHub, discardLogger())
	otherSub := NewSubscriber(client, otherGateway, otherHub, discardLogger())
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go targetSub.Run(subCtx)
	go otherSub.Run(subCtx)
	waitForSubscriber(t, client, gatewayChannel(targetGateway))
	waitForSubscriber(t, client, gatewayChannel(otherGateway))

	publisher := NewPublisher(client)
	if err := publisher.Push(ctx, targetGateway, []uuid.UUID{userID}, []byte(`{"body":"hi"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}

	assertDelivered(t, "target gateway", targetConn)

	select {
	case <-otherConn.send:
		t.Fatalf("other gateway's hub received a push meant for a different gateway's channel")
	case <-time.After(200 * time.Millisecond):
		// expected: nothing arrives
	}
}
