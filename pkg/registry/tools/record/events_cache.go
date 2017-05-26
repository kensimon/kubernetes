/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package record

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/golang/groupcache/lru"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/clock"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/api"
)

const (
	maxLruCacheEntries = 4096

	// if we see the same event that varies only by message
	// more than 10 times in a 10 minute period, aggregate the event
	defaultAggregateMaxEvents         = 10
	defaultAggregateIntervalInSeconds = 600
)

// getEventKey builds unique event key based on source, involvedObject, reason, message
func getEventKey(event *api.Event) string {
	return strings.Join([]string{
		event.Source.Component,
		event.Source.Host,
		event.InvolvedObject.Kind,
		event.InvolvedObject.Namespace,
		event.InvolvedObject.Name,
		string(event.InvolvedObject.UID),
		event.InvolvedObject.APIVersion,
		event.Type,
		event.Reason,
		event.Message,
	},
		"")
}

// EventAggregatorKeyFunc is responsible for grouping events for aggregation
// It returns a tuple of the following:
// aggregateKey - key the identifies the aggregate group to bucket this event
// localKey - key that makes this event in the local group
type EventAggregatorKeyFunc func(event *api.Event) (aggregateKey string, localKey string)

// EventAggregatorByReasonFunc aggregates events by exact match on event.Source, event.InvolvedObject, event.Type and event.Reason
func EventAggregatorByReasonFunc(event *api.Event) (string, string) {
	return strings.Join([]string{
		event.Source.Component,
		event.Source.Host,
		event.InvolvedObject.Kind,
		event.InvolvedObject.Namespace,
		event.InvolvedObject.Name,
		string(event.InvolvedObject.UID),
		event.InvolvedObject.APIVersion,
		event.Type,
		event.Reason,
	},
		""), event.Message
}

// EventAggregatorMessageFunc is responsible for producing an aggregation message
type EventAggregatorMessageFunc func(event *api.Event) string

// EventAggregratorByReasonMessageFunc returns an aggregate message by prefixing the incoming message
func EventAggregatorByReasonMessageFunc(event *api.Event) string {
	return "(combined from similar events): " + event.Message
}

// EventAggregator identifies similar events and aggregates them into a single event
type EventAggregator struct {
	sync.RWMutex

	// The cache that manages aggregation state
	cache *lru.Cache

	// The function that groups events for aggregation
	keyFunc EventAggregatorKeyFunc

	// The function that generates a message for an aggregate event
	messageFunc EventAggregatorMessageFunc

	// The maximum number of events in the specified interval before aggregation occurs
	maxEvents uint

	// The amount of time in seconds that must transpire since the last occurrence of a similar event before it's considered new
	maxIntervalInSeconds uint

	// clock is used to allow for testing over a time interval
	clock clock.Clock
}

// NewEventAggregator returns a new instance of an EventAggregator
func NewEventAggregator(lruCacheSize int, keyFunc EventAggregatorKeyFunc, messageFunc EventAggregatorMessageFunc,
	maxEvents int, maxIntervalInSeconds int, clock clock.Clock) *EventAggregator {
	return &EventAggregator{
		cache:                lru.New(lruCacheSize),
		keyFunc:              keyFunc,
		messageFunc:          messageFunc,
		maxEvents:            uint(maxEvents),
		maxIntervalInSeconds: uint(maxIntervalInSeconds),
		clock:                clock,
	}
}

// aggregateRecord holds data used to perform aggregation decisions
type aggregateRecord struct {
	// we track the number of unique local keys we have seen in the aggregate set to know when to actually aggregate
	// if the size of this set exceeds the max, we know we need to aggregate
	localKeys sets.String
	// The last time at which the aggregate was recorded
	lastTimestamp metav1.Time
}

// EventAggregate checks if a similar event has been seen according to the
// aggregation configuration (max events, max interval, etc) and returns:
//
// - The (potentially modified) event that should be created
// - The cache key for the event, for correlation purposes. This will be set to
//   the full key for normal events, and to the result of
//   EventAggregatorMessageFunc for aggregate events.
func (e *EventAggregator) EventAggregate(newEvent *api.Event) (*api.Event, string) {
	now := metav1.NewTime(e.clock.Now())
	var record aggregateRecord
	// eventKey is the full cache key for this event
	eventKey := getEventKey(newEvent)
	// aggregateKey is for the aggregate event, if one is needed.
	aggregateKey, localKey := e.keyFunc(newEvent)

	// Do we have a record of similar events in our cache?
	e.Lock()
	defer e.Unlock()
	value, found := e.cache.Get(aggregateKey)
	if found {
		record = value.(aggregateRecord)
	}

	// Is the previous record too old? If so, make a fresh one. Note: if we didn't
	// find a similar record, its lastTimestamp will be the zero value, so we
	// create a new one in that case.
	maxInterval := time.Duration(e.maxIntervalInSeconds) * time.Second
	interval := now.Time.Sub(record.lastTimestamp.Time)
	if interval > maxInterval {
		record = aggregateRecord{localKeys: sets.NewString()}
	}

	// Write the new event into the aggregation record and put it on the cache
	record.localKeys.Insert(localKey)
	record.lastTimestamp = now
	e.cache.Add(aggregateKey, record)

	// If we are not yet over the threshold for unique events, don't correlate them
	if uint(record.localKeys.Len()) < e.maxEvents {
		return newEvent, eventKey
	}

	// do not grow our local key set any larger than max
	record.localKeys.PopAny()

	// create a new aggregate event, and return the aggregateKey as the cache key
	// (so that it can be overwritten.)
	eventCopy := &api.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%v.%x", newEvent.InvolvedObject.Name, now.UnixNano()),
			Namespace: newEvent.Namespace,
		},
		Count:          1,
		FirstTimestamp: now,
		InvolvedObject: newEvent.InvolvedObject,
		LastTimestamp:  now,
		Message:        e.messageFunc(newEvent),
		Type:           newEvent.Type,
		Reason:         newEvent.Reason,
		Source:         newEvent.Source,
	}
	return eventCopy, aggregateKey
}

// eventLog records data about when an event was observed
type eventLog struct {
	// The number of times the event has occurred since first occurrence.
	count uint

	// The time at which the event was first recorded.
	firstTimestamp metav1.Time

	// The unique name of the first occurrence of this event
	name string

	// Resource version returned from previous interaction with server
	resourceVersion string
}

// eventLogger logs occurrences of an event
type eventLogger struct {
	sync.RWMutex
	cache *lru.Cache
	clock clock.Clock
}

// newEventLogger observes events and counts their frequencies
func newEventLogger(lruCacheEntries int, clock clock.Clock) *eventLogger {
	return &eventLogger{cache: lru.New(lruCacheEntries), clock: clock}
}

// eventObserve records an event, or updates an existing one if key is a cache hit
func (e *eventLogger) eventObserve(newEvent *api.Event, key string) (string, EventBumpFunc) {
	e.Lock()
	defer e.Unlock()

	// Check if there is an existing event we should update
	lastObservation := e.lastEventObservationFromCache(key)
	glog.Infof("XXX Observing event: %v, observation: %v", newEvent.Name, lastObservation)
	// If not, record the observation and do nothing.
	if lastObservation.count == 0 {
		e.addEventToCache(key, newEvent)
		return "", nil
	}

	// The event is a duplicate of an existing event, bump its count and
	// prepare an update function
	eventCopy := *newEvent
	eventCopy.Count = int32(lastObservation.count + 1)

	// record our new observation
	e.addEventToCache(key, &eventCopy)

	updateFunc := func(existingEvent *api.Event) (*api.Event, error) {
		// If the existing event is a zero-value, we must be creating
		// an event, which can happen if the event's TTL expired with
		// the event still in our cache.
		if existingEvent.Count == 0 {
			return newEvent, nil
		}

		// Ensure we keep the event name the same so it will replace
		// the existing one.
		eventCopy.Name = existingEvent.Name

		// If the event that's actually stored has a newer timestamp
		// than this one, use that event's timestamp (we don't want to
		// rewind time)
		if eventCopy.LastTimestamp.Before(existingEvent.LastTimestamp) {
			eventCopy.LastTimestamp = existingEvent.LastTimestamp
		}

		// If the existing event has the same count as this one or
		// higher, bump the count by one more
		if existingEvent.Count >= eventCopy.Count {
			eventCopy.Count = existingEvent.Count + 1
		}

		// Make sure to observe that we've seen the (modified) event in
		// case the count changed
		e.addEventToCache(key, &eventCopy)

		return &eventCopy, nil
	}

	// Return the name of the last event observation, since that's the
	// event that should be updated
	return lastObservation.name, updateFunc
}

// updateState updates its internal tracking information based on latest server state
func (e *eventLogger) updateState(event *api.Event) {
	key := getEventKey(event)
	e.Lock()
	defer e.Unlock()
	// record our new observation
	e.addEventToCache(key, event)
}

// lastEventObservationFromCache returns the event from the cache, reads must be protected via external lock
func (e *eventLogger) lastEventObservationFromCache(key string) eventLog {
	value, ok := e.cache.Get(key)
	if ok {
		observationValue, ok := value.(eventLog)
		if ok {
			return observationValue
		}
	}
	return eventLog{}
}

// addEventToCache will add the given event to the cache by copying its
// metadata in.  Must be protected via an external lock
func (e *eventLogger) addEventToCache(key string, event *api.Event) {
	e.cache.Add(
		key,
		eventLog{
			count:           uint(event.Count),
			firstTimestamp:  event.FirstTimestamp,
			name:            event.Name,
			resourceVersion: event.ResourceVersion,
		},
	)
}

// EventCorrelator processes all incoming events and performs analysis to avoid overwhelming the system.  It can filter all
// incoming events to see if the event should be filtered from further processing.  It can aggregate similar events that occur
// frequently to protect the system from spamming events that are difficult for users to distinguish.  It performs de-duplication
// to ensure events that are observed multiple times are compacted into a single event with increasing counts.
type EventCorrelator struct {
	// the object that performs event aggregation
	aggregator *EventAggregator
	// the object that observes events as they come through
	logger *eventLogger
}

// NewEventCorrelator returns an EventCorrelator configured with default values.
//
// The EventCorrelator is responsible for event filtering, aggregating, and counting
// prior to interacting with the API server to record the event.
//
// The default behavior is as follows:
//   * No events are filtered from being recorded
//   * Aggregation is performed if a similar event is recorded 10 times in a
//     in a 10 minute rolling interval.  A similar event is an event that varies only by
//     the Event.Message field.  Rather than recording the precise event, aggregation
//     will create a new event whose message reports that it has combined events with
//     the same reason.
//   * Events are incrementally counted if the exact same event is encountered multiple
//     times.
func NewEventCorrelator(clock clock.Clock) *EventCorrelator {
	cacheSize := maxLruCacheEntries
	return &EventCorrelator{
		aggregator: NewEventAggregator(
			cacheSize,
			EventAggregatorByReasonFunc,
			EventAggregatorByReasonMessageFunc,
			defaultAggregateMaxEvents,
			defaultAggregateIntervalInSeconds,
			clock),

		logger: newEventLogger(cacheSize, clock),
	}
}

// EventCorrelate filters, aggregates, counts, and de-duplicates all incoming events
func (c *EventCorrelator) EventCorrelate(newEvent *api.Event) (string, EventBumpFunc) {
	aggregateEvent, ckey := c.aggregator.EventAggregate(newEvent)
	return c.logger.eventObserve(aggregateEvent, ckey)
}

// UpdateState based on the latest observed state from server
func (c *EventCorrelator) UpdateState(event *api.Event) {
	c.logger.updateState(event)
}

// EventBumpFunc is a function that will take an event and bump its timestamps,
// message, and count, according to what should be bumped when an aggregated
// event is seen multiple times.
type EventBumpFunc func(*api.Event) (*api.Event, error)
