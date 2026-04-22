package bot

import (
	"fmt"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// RenderSoftWarn produces the trigger-time soft warning. Currently only the
// NoWorkers verdict is rendered; other verdicts return "" (caller should not
// post empty messages).
func RenderSoftWarn(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":warning: 目前沒有 worker 在線，你仍可繼續選擇，送出時會再確認一次。"
}

// RenderHardReject produces the submit-time rejection message.
func RenderHardReject(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":x: 目前沒有 worker 在線，無法處理。請稍後再試。"
}

// RenderBusyHint produces the suffix appended to the lifecycle queue
// message when the verdict is BusyEnqueueOK with a non-zero ETA.
func RenderBusyHint(v queue.Verdict) string {
	if v.EstimatedWait <= 0 {
		return ""
	}
	return fmt.Sprintf("(預估等候 ~%dm)",
		int(v.EstimatedWait.Round(time.Minute).Minutes()))
}
