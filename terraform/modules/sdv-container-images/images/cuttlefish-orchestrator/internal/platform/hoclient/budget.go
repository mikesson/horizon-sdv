// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hoclient

import (
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
)

// A verb served on the request path costs three serial parts: the POST
// (bounded by CallBudget), the operation wait, and whatever the handler does
// with the result. All three must fit the connection deadline its route was
// classified into. That deadline is stamped at request ENTRY, before
// routing, so a wait bound chosen against it directly is wrong by however much
// the other two parts consume. Overrunning it does not protect the request: it
// breaks the response mid-flight and recycles the serving instance while the
// host completes the work regardless.
//
// So a tier names the envelope it has to fit and DERIVES its wait from it:
//
//	wait = envelope − CallBudget − post-wait reserve
//
// Getting this backwards is not hypothetical. A screenshot is exempt from the
// slow request class on the grounds that its own wait budget keeps it well
// inside the ordinary deadline. That is true of a 20s wait considered alone and
// false of the request: 20s of POST ahead of it puts the worst case at 40s
// against a 30s deadline.
const (
	// InteractiveVerbEnvelope is the ordinary request deadline (the server's
	// Server.WriteTimeout). Its verbs (stop, powerbtn, recording start/stop,
	// screenshot) are bounded work that never moves to the queue, so this is
	// their permanent envelope, including after the slow class retires and they
	// fall back to it. Sizing for the tighter of the two deadlines now is what
	// keeps that retirement a deletion rather than a re-derivation.
	InteractiveVerbEnvelope = 30 * time.Second

	// ExtendedVerbEnvelope is the slow-class deadline (SlowRequestTimeout). Its
	// verbs legitimately take minutes: start, restart and powerwash boot
	// Android, and a bugreport with an ADB capture measures ~42s. Each loses
	// its request-path envelope as it moves to the queue, where nothing holds a
	// client connection.
	ExtendedVerbEnvelope = 290 * time.Second

	// interactiveHandlerReserve covers response marshaling and the row write a
	// lifecycle verb commits after its wait. Nothing in this tier touches
	// storage.
	interactiveHandlerReserve = 2 * time.Second

	// extendedHandlerReserve covers what a handler does AFTER its wait returns.
	// The costly member is bugreport, whose service then streams the artifact
	// off the host and into object storage, and a stream sits outside
	// CallBudget's remit by design. This is therefore a reserve sized on a
	// measured 5.6 MB artifact, NOT a bound: a large enough capture still
	// overruns the envelope. Bounding it belongs with that verb's move to the
	// queue.
	extendedHandlerReserve = 30 * time.Second

	// minWaitLimit floors a derived wait so a client configured with a
	// CallBudget wider than its own envelope degrades to a short wait rather
	// than to an already-expired context.
	minWaitLimit = 1 * time.Second
)

// waitBudget is a verb's operation-wait bound expressed as the request envelope
// it must fit, together with the tier an expiry is reported under. A zero
// envelope means the verb is not on the request path at all and MaxWait is its
// only bound.
//
// ceiling overrides the client's MaxWait for a verb whose cost depends on what
// it was asked to do rather than on the client it was asked through, which
// means a group create, whose wait covers N guests booting. Zero takes MaxWait,
// which is every other verb.
type waitBudget struct {
	envelope time.Duration
	reserve  time.Duration
	ceiling  time.Duration
	tier     string
}

var (
	// interactiveBudget and extendedBudget are the two request-path tiers.
	interactiveBudget = waitBudget{envelope: InteractiveVerbEnvelope, reserve: interactiveHandlerReserve, tier: "interactive"}
	extendedBudget    = waitBudget{envelope: ExtendedVerbEnvelope, reserve: extendedHandlerReserve, tier: "extended"}

	// queuedBudget is for waits the task queue drives, where the bound that
	// matters is the worker's HandleTimeout rather than any client connection.
	// It is named explicitly so a wait running for the whole MaxWait window
	// is a decision on the record instead of an omission.
	queuedBudget = waitBudget{tier: "queued"}
)

// createBudget is queuedBudget with a ceiling sized for the group being
// created. It never shortens a client's configured MaxWait, so an operator who
// widened the ceiling keeps it. It only ensures a group gets at least the
// window its instance count earns.
func (c *HostClient) createBudget(instances int) waitBudget {
	b := queuedBudget
	b.ceiling = max(c.maxWait(), device.CreateProgressWindow(instances))
	return b
}

// waitLimit returns the wait bound this client applies for b: the tier's
// envelope less the POST that precedes the wait and the handler work that
// follows it, never exceeding the client's own MaxWait ceiling. Zero means no
// bound beyond MaxWait.
//
// The POST term reads the client's configured CallBudget rather than the
// default, so a client tuned for a slow fleet stays inside its envelope instead
// of silently spending the reserve. A CallBudget explicitly disabled (< 0)
// forfeits the envelope guarantee, because an unbounded POST cannot be reserved
// against, and is clamped to zero here. It is never credited back to the wait.
func (c *HostClient) waitLimit(b waitBudget) time.Duration {
	if b.envelope == 0 {
		return 0
	}
	limit := max(b.envelope-max(c.callBudget(), 0)-b.reserve, minWaitLimit)
	if ceiling := c.waitCeiling(b); ceiling < limit {
		return ceiling
	}
	return limit
}

// waitCeiling is the loop deadline for b: the budget's own ceiling when it
// names one, else the client's MaxWait.
func (c *HostClient) waitCeiling(b waitBudget) time.Duration {
	if b.ceiling > 0 {
		return b.ceiling
	}
	return c.maxWait()
}
