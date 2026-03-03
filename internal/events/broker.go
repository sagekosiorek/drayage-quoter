package events

import "sync"

// Broker is a simple in-process pub/sub hub keyed by topic string.
// Subscribers receive messages via unbuffered channels; Publish is non-blocking
// and drops messages to slow consumers rather than blocking the publisher.
type Broker struct {
	mu   sync.Mutex
	subs map[string][]chan string
}

// Subscribe registers a new channel for the given topic and returns it.
// The caller must call Unsubscribe when done to avoid leaking goroutines.
func (b *Broker) Subscribe(topic string) chan string {
	ch := make(chan string, 4)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[string][]chan string)
	}
	b.subs[topic] = append(b.subs[topic], ch)
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a channel from the given topic and closes it.
func (b *Broker) Unsubscribe(topic string, ch chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subs[topic]
	for i, s := range subs {
		if s == ch {
			b.subs[topic] = append(subs[:i], subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Publish sends msg to all subscribers on topic. Non-blocking: slow consumers
// have their messages dropped rather than stalling the caller.
func (b *Broker) Publish(topic, msg string) {
	b.mu.Lock()
	subs := make([]chan string, len(b.subs[topic]))
	copy(subs, b.subs[topic])
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
		}
	}
}
