package router

import (
	"container/list"
	"net/http"
	"sync"
	"time"
)

type stickyEntry struct {
	providerID    string
	upstreamModel string
}

// cooldownKey scopes a 429 cooldown to one provider binding of one model, so a
// rate limit on one model does not penalise the same provider for other models.
type cooldownKey struct {
	modelID    string
	providerID string
}

// State is the routing memory that is independent of a configuration snapshot:
// conversation pins, 429 cooldowns and round-robin cursors. App owns one State
// and hands it to every Router it builds, so an administrative change reloads
// the configuration without moving running conversations to another provider or
// restarting every rotation at the first candidate.
//
// A State is safe for concurrent use and its caches are bounded: pins are an LRU
// of stickyCacheSize entries, and cooldowns and cursors are keyed by model and
// provider identifiers, so they are bounded by the configured catalog.
type State struct {
	mu         sync.Mutex
	sticky     map[string]stickyEntry
	order      *list.List
	elements   map[string]*list.Element
	cooldown   map[cooldownKey]time.Time
	roundRobin map[string]uint64
}

// NewState builds an empty routing memory.
func NewState() *State {
	return &State{
		sticky:     make(map[string]stickyEntry),
		order:      list.New(),
		elements:   make(map[string]*list.Element),
		cooldown:   make(map[cooldownKey]time.Time),
		roundRobin: make(map[string]uint64),
	}
}

func (s *State) routeOrder(candidates []candidate, requestID, modelID, routing string, rand func() float64) []candidate {
	s.mu.Lock()
	defer s.mu.Unlock()

	if requestID != "" {
		if entry, ok := s.sticky[requestID]; ok {
			for index, item := range candidates {
				if item.modelProvider.ProviderID == entry.providerID &&
					item.modelProvider.UpstreamModel == entry.upstreamModel {
					s.touchLocked(requestID)
					ordered := make([]candidate, 0, len(candidates))
					ordered = append(ordered, item)
					ordered = append(ordered, candidates[:index]...)
					ordered = append(ordered, candidates[index+1:]...)
					return s.demoteCoolingLocked(ordered, modelID, time.Now())
				}
			}
			// The pinned upstream cannot serve this request any more.
			s.removeLocked(requestID)
		}
	}

	ordered := s.strategyOrderLocked(candidates, modelID, routing, rand)
	// Sticky state is written only once a provider has actually served the
	// request, so a tentative pick that is about to fail cannot be observed by
	// a concurrent request carrying the same id.
	return s.demoteCoolingLocked(ordered, modelID, time.Now())
}

// strategyOrderLocked applies the model's routing strategy to a new session.
// The returned slice is always a copy so callers never mutate the candidate
// list built for this request. The lock must be held.
func (s *State) strategyOrderLocked(candidates []candidate, modelID, routing string, rand func() float64) []candidate {
	ordered := append([]candidate(nil), candidates...)
	if len(ordered) < 2 {
		return ordered
	}
	switch routing {
	case RoutingRandom:
		// Fisher-Yates shuffle: every provider is tried once, in random order.
		for index := len(ordered) - 1; index > 0; index-- {
			swap := int(rand() * float64(index+1))
			ordered[index], ordered[swap] = ordered[swap], ordered[index]
		}
	case RoutingRoundRobin:
		next := s.roundRobin[modelID]
		s.roundRobin[modelID] = next + 1
		slot := next % uint64(len(ordered))
		// slot is smaller than len(ordered), so the conversion cannot overflow.
		offset := int(slot) //nolint:gosec // bounded by len(ordered)
		if offset != 0 {
			ordered = append(ordered[offset:], ordered[:offset]...)
		}
	}
	// priority keeps the declared provider order.
	return ordered
}

// demoteCoolingLocked moves providers that recently answered 429 for this model
// to the end of the order. Cooldown is a preference rather than a hard ban, so
// a request still tries a cooling provider when every other candidate is
// unavailable. The lock must be held.
func (s *State) demoteCoolingLocked(ordered []candidate, modelID string, now time.Time) []candidate {
	if len(s.cooldown) == 0 || len(ordered) < 2 {
		return ordered
	}
	live := make([]candidate, 0, len(ordered))
	cooling := make([]candidate, 0, len(ordered))
	for _, item := range ordered {
		key := cooldownKey{modelID: modelID, providerID: item.provider.ID}
		if until, ok := s.cooldown[key]; ok {
			if now.Before(until) {
				cooling = append(cooling, item)
				continue
			}
			delete(s.cooldown, key)
		}
		live = append(live, item)
	}
	if len(cooling) == 0 {
		return ordered
	}
	return append(live, cooling...)
}

// startCooldown deprioritises a provider for one model after it reports 429.
// The cooldown follows Retry-After, defaults to 30 s, and is capped at 5 min.
func (s *State) startCooldown(modelID, providerID string, header http.Header) {
	now := time.Now()
	delay, ok := parseRetryAfter(header, now)
	switch {
	case !ok:
		delay = defaultCooldown
	case delay > maxCooldown:
		delay = maxCooldown
	}
	if delay <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldown[cooldownKey{modelID: modelID, providerID: providerID}] = now.Add(delay)
}

// pin records the provider that served one conversation. It is called only
// after a successful upstream reply.
func (s *State) pin(requestID string, entry candidate) {
	if requestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pinLocked(requestID, entry)
}

func (s *State) pinLocked(requestID string, entry candidate) {
	if element, ok := s.elements[requestID]; ok {
		s.order.MoveToBack(element)
	} else {
		s.elements[requestID] = s.order.PushBack(requestID)
	}
	s.sticky[requestID] = stickyEntry{
		providerID:    entry.modelProvider.ProviderID,
		upstreamModel: entry.modelProvider.UpstreamModel,
	}
	for s.order.Len() > stickyCacheSize {
		front := s.order.Front()
		if front == nil {
			break
		}
		s.order.Remove(front)
		evicted, _ := front.Value.(string)
		delete(s.elements, evicted)
		delete(s.sticky, evicted)
	}
}

func (s *State) touchLocked(requestID string) {
	if element, ok := s.elements[requestID]; ok {
		s.order.MoveToBack(element)
	}
}

func (s *State) removeLocked(requestID string) {
	if element, ok := s.elements[requestID]; ok {
		s.order.Remove(element)
		delete(s.elements, requestID)
	}
	delete(s.sticky, requestID)
}

// PruneModels drops cooldowns and rotation cursors for models that no longer
// exist. A State now lives for the whole process, so deleted models must not
// leave entries behind forever. Conversation pins are bounded by the LRU and are
// left to age out.
func (s *State) PruneModels(keep map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.cooldown {
		if !keep[key.modelID] {
			delete(s.cooldown, key)
		}
	}
	for modelID := range s.roundRobin {
		if !keep[modelID] {
			delete(s.roundRobin, modelID)
		}
	}
}

// unpin drops a conversation's pin after a failed request, so the next turn is
// free to pick another provider.
func (s *State) unpin(requestID string) {
	if requestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(requestID)
}
