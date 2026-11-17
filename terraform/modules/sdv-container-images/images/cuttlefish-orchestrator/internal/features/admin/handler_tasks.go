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

package adminfeature

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/task"
)

// TaskAdmin exposes dead-letter queue operations and queue observability; the
// Spanner TaskQueue satisfies it.
type TaskAdmin interface {
	ListFailed(ctx context.Context, limit int, resourceID string) ([]*task.Task, error)
	ListLive(ctx context.Context, limit int) ([]*task.Task, error)
	CountByTypeState(ctx context.Context) ([]task.TypeStateCount, error)
	RequeueFailed(ctx context.Context, taskID string) error
	DeleteTask(ctx context.Context, taskID string) error
	Stats(ctx context.Context) (task.QueueStats, error)
}

// FailedTaskItem is a dead-lettered task in the admin list.
type FailedTaskItem struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	ResourceID  string    `json:"resource_id"`
	Attempts    int       `json:"attempts"`
	MaxAttempts int       `json:"max_attempts"`
	LastError   string    `json:"last_error"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListFailedTasksInput is the query for listing dead-lettered tasks.
type ListFailedTasksInput struct {
	Limit      int    `query:"limit" minimum:"1" maximum:"500" default:"100" doc:"Maximum tasks to return"`
	ResourceID string `query:"resource_id" doc:"Only tasks for this resource (device/host ID)"`
}

// ListFailedTasksOutput is the dead-letter list response.
type ListFailedTasksOutput struct {
	Body struct {
		Tasks []FailedTaskItem `json:"tasks"`
	}
}

// TaskIDInput addresses a single task by ID.
type TaskIDInput struct {
	ID string `path:"id" doc:"Task ID"`
}

// TaskStatsInput is the input for queue statistics.
type TaskStatsInput struct{}

// TaskStatsOutput is a point-in-time snapshot of task queue depth.
type TaskStatsOutput struct {
	Body struct {
		Pending                 int64 `json:"pending" doc:"Tasks awaiting a worker"`
		Leased                  int64 `json:"leased" doc:"Tasks currently held by a worker"`
		Failed                  int64 `json:"failed" doc:"Dead-lettered tasks"`
		OldestPendingAgeSeconds int64 `json:"oldest_pending_age_seconds" doc:"Age of the oldest pending task (0 if none)"`
	}
}

// TaskActionOutput is the result of a retry/delete action.
type TaskActionOutput struct {
	Body struct {
		Status string `json:"status"`
	}
}

// ListFailedTasks returns dead-lettered tasks for operator review.
func (h *AdminHandler) ListFailedTasks(ctx context.Context, input *ListFailedTasksInput) (*ListFailedTasksOutput, error) {
	if err := h.requireTaskAdmin(); err != nil {
		return nil, err
	}
	tasks, err := h.taskAdmin.ListFailed(ctx, input.Limit, input.ResourceID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to list dead-lettered tasks", err)
	}
	out := &ListFailedTasksOutput{}
	out.Body.Tasks = make([]FailedTaskItem, 0, len(tasks))
	for _, t := range tasks {
		out.Body.Tasks = append(out.Body.Tasks, FailedTaskItem{
			ID:          t.ID,
			Type:        t.Type,
			ResourceID:  t.ResourceID,
			Attempts:    t.Attempts,
			MaxAttempts: t.MaxAttempts,
			LastError:   t.LastError,
			UpdatedAt:   t.UpdatedAt,
		})
	}
	return out, nil
}

// TaskStats returns a queue-depth snapshot for the operations view.
func (h *AdminHandler) TaskStats(ctx context.Context, _ *TaskStatsInput) (*TaskStatsOutput, error) {
	if err := h.requireTaskAdmin(); err != nil {
		return nil, err
	}
	stats, err := h.taskAdmin.Stats(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to read queue statistics", err)
	}
	out := &TaskStatsOutput{}
	out.Body.Pending = stats.Pending
	out.Body.Leased = stats.Leased
	out.Body.Failed = stats.Failed
	out.Body.OldestPendingAgeSeconds = int64(stats.OldestPendingAge.Seconds())
	return out, nil
}

// RetryTask resets a dead-lettered task to pending for another attempt.
func (h *AdminHandler) RetryTask(ctx context.Context, input *TaskIDInput) (*TaskActionOutput, error) {
	if err := h.requireTaskAdmin(); err != nil {
		return nil, err
	}
	if err := h.taskAdmin.RequeueFailed(ctx, input.ID); err != nil {
		if errors.Is(err, task.ErrRequeueLeadsNowhere) {
			return nil, huma.Error409Conflict(
				"this task cannot be usefully requeued: its resource already reached a terminal status, "+
					"so the handler would no-op. Retry the host via POST /v2/hosts/{id}/retry "+
					"or recreate the device instead", err)
		}
		if errors.Is(err, task.ErrRequeueUnsafe) {
			return nil, huma.Error409Conflict(
				"this task carries an operation handle and cannot be requeued: it would re-actuate the verb "+
					"with its retry budget reset and no operation tracking the result, so a destructive verb "+
					"could run twice and a harmless one would report an outcome nobody can see. "+
					"Issue a new request for the verb on the device instead", err)
		}
		return nil, huma.Error400BadRequest("failed to retry task", err)
	}
	audit.LogChange(ctx, h.auditLogger, audit.ActionTaskRetry, audit.ResourceTask, input.ID, nil)
	out := &TaskActionOutput{}
	out.Body.Status = "requeued"
	return out, nil
}

// DeleteTask force-removes a task from the queue regardless of state.
func (h *AdminHandler) DeleteTask(ctx context.Context, input *TaskIDInput) (*TaskActionOutput, error) {
	if err := h.requireTaskAdmin(); err != nil {
		return nil, err
	}
	if err := h.taskAdmin.DeleteTask(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete task", err)
	}
	audit.LogChange(ctx, h.auditLogger, audit.ActionTaskDelete, audit.ResourceTask, input.ID, nil)
	out := &TaskActionOutput{}
	out.Body.Status = "deleted"
	return out, nil
}

func (h *AdminHandler) requireTaskAdmin() error {
	if h.taskAdmin == nil {
		return huma.Error500InternalServerError("task admin is not configured")
	}
	return nil
}
