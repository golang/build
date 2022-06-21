package relui

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
)

func TestAwaitFunc(t *testing.T) {
	cases := []struct {
		desc       string
		want       map[string]interface{}
		wantErr    bool
		wantCancel bool
	}{
		{
			desc: "success",
			want: map[string]interface{}{"await": true},
		},
		{
			desc:    "error",
			wantErr: true,
		},
		{
			desc:       "cancel",
			wantCancel: true,
			wantErr:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			didWork := make(chan struct{}, 2)
			success := make(chan interface{})
			done := make(chan interface{})
			cond := func(ctx *workflow.TaskContext) (bool, error) {
				select {
				case <-success:
					if c.wantCancel {
						cancel()
						return false, ctx.Err()
					} else if c.wantErr {
						return false, errors.New("someError")
					}
					return true, nil
				case <-ctx.Done():
					return false, ctx.Err()
				case didWork <- struct{}{}:
					return false, nil
				}
			}
			wd := workflow.New()
			await := wd.Task("AwaitFunc", AwaitFunc, wd.Constant(10*time.Millisecond), wd.Constant(cond))
			wd.Output("await", await)

			w, err := workflow.Start(wd, nil)
			if err != nil {
				t.Fatalf("workflow.Start(%v, %v) = %v, %v, wanted no error", wd, nil, w, err)
			}
			go func() {
				outputs, err := runWorkflow(t, ctx, w, nil)
				if diff := cmp.Diff(c.want, outputs); diff != "" {
					t.Errorf("runWorkflow() mismatch (-want +got):\n%s", diff)
				}
				if (err != nil) != c.wantErr {
					t.Errorf("runworkflow() = _, %v, wantErr: %v", err, c.wantErr)
				}
				close(done)
			}()

			select {
			case <-time.After(5 * time.Second):
				t.Error("AwaitFunc() never called f, wanted at least one call")
			case <-didWork:
				// AwaitFunc() called f successfully.
			}
			select {
			case <-done:
				t.Errorf("AwaitFunc() finished early, wanted it to still be looping")
			case <-didWork:
				close(success)
			}
			<-done
		})
	}
}

func TestCheckTaskApproved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hourAgo := time.Now().Add(-1 * time.Hour)
	p := testDB(ctx, t)
	q := db.New(p)

	wf := db.CreateWorkflowParams{
		ID:        uuid.New(),
		Params:    nullString(`{"farewell": "bye", "greeting": "hello"}`),
		Name:      nullString(`echo`),
		CreatedAt: hourAgo,
		UpdatedAt: hourAgo,
	}
	if _, err := q.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow(_, %v) = _, %v, wanted no error", wf, err)
	}
	gtg := db.CreateTaskParams{
		WorkflowID: wf.ID,
		Name:       "APPROVE-please",
		Finished:   true,
		Error:      nullString("internal explosion"),
		CreatedAt:  hourAgo,
		UpdatedAt:  hourAgo,
	}
	if _, err := q.CreateTask(ctx, gtg); err != nil {
		t.Fatalf("CreateTask(_, %v) = _, %v, wanted no error", gtg, err)
	}
	tctx := &workflow.TaskContext{Context: ctx, WorkflowID: wf.ID}

	got, err := checkTaskApproved(tctx, p, gtg.Name)
	if err != nil || got {
		t.Errorf("checkTaskApproved(_, %v, %q) = %t, %v wanted %t, %v", p, gtg.Name, got, err, false, nil)
	}

	atp := db.ApproveTaskParams{
		WorkflowID: wf.ID,
		Name:       gtg.Name,
		ApprovedAt: sql.NullTime{Time: time.Now(), Valid: true},
	}
	_, err = q.ApproveTask(ctx, atp)
	if err != nil {
		t.Errorf("q.ApproveTask(_, %v) = _, %v, wanted no error", atp, err)
	}

	got, err = checkTaskApproved(tctx, p, gtg.Name)
	if err != nil || !got {
		t.Errorf("checkTaskApproved(_, %v, %q) = %t, %v wanted %t, %v", p, gtg.Name, got, err, true, nil)
	}
}

func runWorkflow(t *testing.T, ctx context.Context, w *workflow.Workflow, listener workflow.Listener) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	t.Helper()
	if listener == nil {
		listener = &verboseListener{t, nil}
	}
	return w.Run(ctx, listener)
}
