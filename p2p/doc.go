/*
Package p2p provides header exchange utilities that enable requesting and serving headers over direct peer-to-peer streams. It primarily provides:

# Components

Exchange Server, Exchange Client and GossipSub Subscriber for header exchange.

  - Exchange Server:
    The exchange server provides handlers for getting latest or head headers, or getting headers by hash or range.
    On start, the server it registers a streamHandler on the protocolID ("/${networkID}/header-ex/v0.0.1"), where networkID can be
    any available network.
    The server is powered by a header.Store[H] implementation that is used to store and retrieve headers.

  - Exchange Client:
    The exchange client provides functionality to retrieve latest/head headers, headers by hash or range.
    On start the client connects to trusted peers (bootstrappers) as well as kicks off tracking and garbage collection routines for peer connections.
    The getters it defines are several and are used to get headers by height, range, and hash.
    The exchange client uses bi-directional read-write libp2p streams to request and receive headers from peers.
    The peers that the client interacts with are tracked by the client's peerTracker, and they are of two types:

    Trusted Peers: These are bootstrapper peers that are trusted by the client and are used to request headers.

    Connected Peers: Peers that were not supplied as TrustedPeers but got connected to the node down the line.
    The peer manager provides the most available peers to the client's session for requesting headers by using an availability queue
    with negative sorting. The availability queue is updated by the client's peerTracker on every peer connection/disconnection.

    To learn more about the peer usage, see https://github.com/celestiaorg/go-header/blobl/main/docs/peer-usage-p2p-header.png

  - Subscriber:
    The subscriber is an abstraction over GossipSub subscriptions that tracks the pubsub object + topic.
    On start, the subscriber joins the GossipSub topic: "${networkID}/header-ex/v0.0.3" to listen for incoming headers.
    The subscriber provides methods for adding incomning messages' validators* as well as methods that subscribe to the gossipsub topic.
    It also defines a broadcast method to publish headers to other subscribers.
    *) Validators are functions that are used to validate incoming messages before
    they are processed by the subscriber. They either return reject or accept the incoming message.

For more information, see the documentation for each component.

# Usage	Examples

<details><summary>sss</summary>hshs</details>

Exchange Server Usage:
To use the exchange server, first create a new instance of the server and start it:

	s, err:= p2p.NewExchangeServer[H](
		libp2pHost,
		store,
		WithNetworkID[ServerParameters](networkID),
	)
	err = s.Start()
	// ...
	err = s.Stop()

Exchange Client Usage: To use the exchange client, first create a new instance of the client and start it:

	c, err := p2p.NewExchange[H](
		libp2pHost,
		trustedPeers,
		libp2pConnGater,
		WithChainId(chainID),
	)
	err = c.Start()

Then, you can use the various getters to get headers:

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// Get the best head from trusted peers
	head, err := c.Head(ctx)

	// Get a header by height
	header, err := c.GetByHeight(ctx, height)

	// Get a range of headers by height
	headers, err := c.GetRangeByHeight(ctx, start, end)

	// Get a range of verified headers by height
	headers, err := c.GetVerifiedRange(ctx, header, amount)

	// Get a header by hash
	header, err := c.Get(ctx, header.hash)

Subscriber Usage: To use the subscriber, first create a new instance of the subscriber and start it:

	sub := p2p.NewSubscriber[H](
		pubsub, // pubsub.PubSub from libp2p
		msdIDFn, // message ID signing function
		networkID, // network ID
	)
	err := sub.Start()

Then, you can add validators and subscribe to headers:

	// Add a validator
	err := sub.AddValidator(func(ctx context.Context, header H) pubsub.ValidationResult {
		if msg.ValidatorData != nil {
			return true
		}
		return false
	})

	// Subscribe to headers
	subscription, err := sub.Subscribe(ctx)

	// Keep listening for new headers

		go func() {
			for {
				select {
				case <-ctx.Done():
					subscription.Cancel()
					return
				case header := subscription.NextHeader(ctx):
					// Do something with the header
				}
			}
		}()

	// Broadcast a header
	err := sub.Broadcast(header)
*/
package p2p
