package probe

import (
	"fmt"
	"sort"
	"time"
)

const competitiveAIHitRetryInterval = 500 * time.Millisecond
const competitiveAIHitMaxAttempts = 6

type competitiveAIHitRetry struct {
	key       liveCompetitiveAIHitRequestKey
	payload   []byte
	exhausted bool
}

// Called under the room actor/runtime lock, including when the scene clock is
// stationary. Keep native message indexes and FA5 identity unchanged. The UDP
// sender allocates a fresh outer packet number for every retransmission.
func (runtime *liveCompetitiveAIRuntime) dueHitRetries(now time.Time) []competitiveAIHitRetry {
	runtime.hitRequestMu.Lock()
	defer runtime.hitRequestMu.Unlock()
	var due []competitiveAIHitRetry
	for key, request := range runtime.hitRequests {
		if request.state != competitiveAIHitPending || now.Sub(request.lastSentAt) < competitiveAIHitRetryInterval {
			continue
		}
		retry := competitiveAIHitRetry{key: key, payload: request.payload}
		if request.attempts >= competitiveAIHitMaxAttempts {
			request.state = competitiveAIHitRecoveryRequired
			retry.exhausted = true
		} else {
			request.attempts++
			request.lastSentAt = now
		}
		runtime.hitRequests[key] = request
		due = append(due, retry)
	}
	sort.Slice(due, func(i, j int) bool { return due[i].key.playerID < due[j].key.playerID })
	return due
}

func (runtime *liveCompetitiveAIRuntime) retryHitRequests(server *Server, now time.Time) {
	for _, retry := range runtime.dueHitRetries(now) {
		if retry.exhausted {
			// Do not fabricate a miss or a death on timeout. A late exact authority
			// echo may still reconcile this actor; quarantine is now an explicit
			// transport failure rather than silent policy inactivity.
			server.log(logEvent{Level: "error", Event: "competitive_ai_hit_recovery_required", RoomID: fmt.Sprint(runtime.roomID), Result: fmt.Sprintf("game_%d_player_%d_time_%d_attempts_%d", runtime.gameID, retry.key.playerID, retry.key.clientTime, competitiveAIHitMaxAttempts)})
			continue
		}
		if err := runtime.sendPeerPayload(server, retry.key.playerID, retry.payload); err != nil {
			server.log(logEvent{Level: "warn", Event: "competitive_ai_hit_retry_failed", RoomID: fmt.Sprint(runtime.roomID), ErrorContext: err.Error()})
		}
	}
}
