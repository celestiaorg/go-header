package p2p

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/celestiaorg/go-header"
)

// SubscriberOption is a functional option for the Subscriber.
type SubscriberOption func(*SubscriberParams)

// SubscriberParams defines the parameters for the Subscriber
// configurable with SubscriberOption.
type SubscriberParams struct {
	networkID string
	metrics   bool
}

// Subscriber manages the lifecycle and relationship of header Module
// with the "header-sub" gossipsub topic.
type Subscriber[H header.Header[H]] struct {
	pubsubTopicID string

	metrics *subscriberMetrics
	pubsub  *pubsub.PubSub
	topic   *pubsub.Topic
	msgID   pubsub.MsgIdFunction

	verifierMu   sync.Mutex
	verifierSema chan struct{} // closed when verifier is set
	verifier     func(context.Context, H) error
}

// WithSubscriberMetrics enables metrics collection for the Subscriber.
func WithSubscriberMetrics() SubscriberOption {
	return func(params *SubscriberParams) {
		params.metrics = true
	}
}

// WithSubscriberNetworkID sets the network ID for the Subscriber.
func WithSubscriberNetworkID(networkID string) SubscriberOption {
	return func(params *SubscriberParams) {
		params.networkID = networkID
	}
}

// NewSubscriber returns a Subscriber that manages the header Module's
// relationship with the "header-sub" gossipsub topic.
func NewSubscriber[H header.Header[H]](
	ps *pubsub.PubSub,
	msgID pubsub.MsgIdFunction,
	opts ...SubscriberOption,
) (*Subscriber[H], error) {
	var params SubscriberParams
	for _, opt := range opts {
		opt(&params)
	}

	var metrics *subscriberMetrics
	if params.metrics {
		var err error
		metrics, err = newSubscriberMetrics()
		if err != nil {
			return nil, err
		}
	}

	return &Subscriber[H]{
		metrics:       metrics,
		pubsubTopicID: PubsubTopicID(params.networkID),
		pubsub:        ps,
		msgID:         msgID,
		verifierSema:  make(chan struct{}),
	}, nil
}

// Start starts the Subscriber and joins the instance's topic. SetVerifier must
// be called separately to ensure a validator is mounted on the topic.
func (s *Subscriber[H]) Start(context.Context) (err error) {
	log.Debugw("joining topic", "topic ID", s.pubsubTopicID)
	err = s.pubsub.RegisterTopicValidator(s.pubsubTopicID, s.verifyMessage)
	if err != nil {
		return err
	}

	topic, err := s.pubsub.Join(s.pubsubTopicID, pubsub.WithTopicMessageIdFn(s.msgID))
	if err != nil {
		return err
	}

	s.topic = topic
	return err
}

// Stop closes the topic and unregisters its validator.
func (s *Subscriber[H]) Stop(context.Context) (err error) {
	err = errors.Join(err, s.metrics.Close())
	// we must close the topic first and then unregister the validator
	// this ensures we never get a message after the validator is unregistered
	err = errors.Join(err, s.topic.Close())
	err = errors.Join(err, s.pubsub.UnregisterTopicValidator(s.pubsubTopicID))
	return err
}

// SetVerifier set given verification func as Header PubSub topic validator.
// Does not punish peers if *header.VerifyError is given with SoftFailure set to true.
func (s *Subscriber[H]) SetVerifier(verifier func(context.Context, H) error) error {
	s.verifierMu.Lock()
	defer s.verifierMu.Unlock()
	if s.verifier != nil {
		return errors.New("verifier already set")
	}

	s.verifier = verifier
	close(s.verifierSema)
	return nil
}

// Subscribe returns a new subscription to the Subscriber's
// topic.
func (s *Subscriber[H]) Subscribe() (header.Subscription[H], error) {
	if s.topic == nil {
		return nil, errors.New(
			"header topic is not instantiated, service must be started before subscribing",
		)
	}

	return newSubscription[H](s.topic, s.metrics)
}

// Broadcast broadcasts the given Header to the topic.
func (s *Subscriber[H]) Broadcast(ctx context.Context, header H, opts ...pubsub.PubOpt) error {
	bin, err := header.MarshalBinary()
	if err != nil {
		return err
	}

	opts = append(opts, pubsub.WithValidatorData(header))
	return s.topic.Publish(ctx, bin, opts...)
}

func (s *Subscriber[H]) verifyMessage(
	ctx context.Context,
	p peer.ID,
	msg *pubsub.Message,
) (res pubsub.ValidationResult) {
	defer func() {
		err := recover()
		if err != nil {
			log.Errorf("PANIC while unmarshalling or verifying header: %s", err)
			res = pubsub.ValidationReject
		}
	}()

	hdr, err := s.extractHeader(msg)
	if err != nil {
		log.Errorw("invalid header",
			"from", p.ShortString(),
			"err", err)
		s.metrics.reject(ctx)
		return pubsub.ValidationReject
	}

	// ensure we have a verifier set before verifying the message
	select {
	case <-s.verifierSema:
	case <-ctx.Done():
		log.Errorw(
			"verifier was not set before incoming header verification",
			"from",
			p.ShortString(),
		)
		s.metrics.ignore(ctx)
		return pubsub.ValidationIgnore
	}

	var verErr *header.VerifyError
	switch err := s.verifier(ctx, hdr); {
	case errors.As(err, &verErr) && verErr.SoftFailure:
		s.metrics.ignore(ctx)
		return pubsub.ValidationIgnore
	case err != nil:
		s.metrics.reject(ctx)
		return pubsub.ValidationReject
	default:
		// keep the valid header in the msg so Subscriptions can access it without
		// additional unmarshalling
		msg.ValidatorData = hdr
		s.metrics.accept(ctx, len(msg.Data))
		return pubsub.ValidationAccept
	}
}

func (s *Subscriber[H]) extractHeader(msg *pubsub.Message) (H, error) {
	if msg.ValidatorData != nil {
		hdr, ok := msg.ValidatorData.(H)
		if !ok {
			panic(fmt.Sprintf("msg ValidatorData is of type %T", msg.ValidatorData))
		}
		return hdr, nil
	}

	hdr := header.New[H]()
	if err := hdr.UnmarshalBinary(msg.Data); err != nil {
		return hdr, err
	}
	if err := hdr.Validate(); err != nil {
		return hdr, err
	}
	return hdr, nil
}
