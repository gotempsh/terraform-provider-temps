// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

// fakeDeployment is an image deployment. Like Temps, it moves from pending
// through running to completed as it is polled, and the completed one
// becomes the environment's current deployment. An image whose name
// contains "broken" fails while pulling; one containing "slow" never
// finishes; one containing "late" completes just as it is cancelled; the first
// read of one containing "unreadable" fails with a 500.
type fakeDeployment struct {
	d    client.Deployment
	req  client.DeployImageRequest
	gets int
}

type fakeDeployments struct {
	byID map[int64]*fakeDeployment
}

func newFakeDeployments() *fakeDeployments {
	return &fakeDeployments{byID: map[int64]*fakeDeployment{}}
}

// advance moves a deployment one step on each read.
func (f *fakeTemps) advanceDeployment(fd *fakeDeployment) {
	fd.gets++
	switch {
	case fd.d.Status == "cancelled" || fd.d.Status == "failed" || fd.d.Status == "completed":
		return
	case strings.Contains(fd.req.ImageRef, "slow"), strings.Contains(fd.req.ImageRef, "late"),
		strings.Contains(fd.req.ImageRef, "unreadable"):
		fd.d.Status = "running"
	case fd.gets == 1:
		fd.d.Status = "running"
	case strings.Contains(fd.req.ImageRef, "broken"):
		fd.d.Status = "failed"
		fd.d.FinishedAt = ptr(client.Timestamp(fakeFinishedAt))
		fd.d.CancelledReason = ptr("manifest unknown")
		fd.d.Failure = &client.DeploymentFailure{Stage: "pull", Code: "image_not_found", Title: "Image tag not found",
			Remediation: "Push the tag, or point the deployment at one that exists.", FailedJob: ptr("pull_image")}
	default:
		f.completeDeployment(fd)
	}
}

const (
	fakeCreatedAt  = "2026-01-01T00:00:00Z"
	fakeFinishedAt = "2026-01-01T00:01:00Z"
)

// completeDeployment makes fd its environment's current deployment.
func (f *fakeTemps) completeDeployment(fd *fakeDeployment) {
	fd.d.Status = "completed"
	fd.d.FinishedAt = ptr(client.Timestamp(fakeFinishedAt))
	for _, o := range f.dep.byID {
		if o.d.EnvironmentID == fd.d.EnvironmentID {
			o.d.IsCurrent = false
		}
	}
	fd.d.IsCurrent = true
}

// epochMillis turns an RFC 3339 time into the milliseconds released Temps
// servers send for deployment times.
func epochMillis(t client.Timestamp) int64 {
	parsed, err := time.Parse(time.RFC3339, string(t))
	if err != nil {
		panic(err)
	}
	return parsed.UnixMilli()
}

// deploymentJSON is GET /projects/{id}/deployments/{id} as released Temps
// servers send it: times in epoch milliseconds.
func deploymentJSON(d client.Deployment) map[string]any {
	out := map[string]any{"id": d.ID, "project_id": d.ProjectID, "environment_id": d.EnvironmentID, "status": d.Status,
		"url": d.URL, "is_current": d.IsCurrent, "created_at": epochMillis(d.CreatedAt), "finished_at": nil,
		"cancelled_reason": d.CancelledReason, "started_at": nil}
	if d.FinishedAt != nil {
		out["finished_at"] = epochMillis(*d.FinishedAt)
	}
	if d.Failure != nil {
		out["failure"] = d.Failure
	}
	return out
}

func (f *fakeTemps) deploymentRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	h("POST /api/projects/{pid}/environments/{eid}/deploy/image", func(w http.ResponseWriter, r *http.Request) {
		pid, eid := pathID(r, "pid"), pathID(r, "eid")
		if _, ok := f.projects[pid]; !ok {
			notFound(w, "project", pid)
			return
		}
		e, ok := f.envs[eid]
		if !ok || e.ProjectID != pid {
			notFound(w, "environment", eid)
			return
		}
		var req client.DeployImageRequest
		if err := decode(r, &req); err != nil || req.ImageRef == "" {
			problem(w, 400, "Missing Image Reference", "image_ref is required")
			return
		}
		if req.HealthCheckPath != nil && !strings.HasPrefix(*req.HealthCheckPath, "/") {
			problem(w, 400, "Invalid Health Check Path", "the health check path must start with '/'")
			return
		}
		id := f.nextID()
		fd := &fakeDeployment{req: req, d: client.Deployment{ID: id, ProjectID: pid, EnvironmentID: eid, Status: "pending",
			URL: e.MainURL, CreatedAt: fakeCreatedAt}}
		f.dep.byID[id] = fd
		writeJSON(w, 202, map[string]any{"id": id, "project_id": pid, "environment_id": eid, "slug": fmt.Sprintf("deployment-%d", id),
			"state": "pending", "source_type": "external_image", "created_at": fd.d.CreatedAt})
	})
	deployment := func(w http.ResponseWriter, r *http.Request) *fakeDeployment {
		fd, ok := f.dep.byID[pathID(r, "did")]
		if !ok || fd.d.ProjectID != pathID(r, "pid") {
			problem(w, 404, "Deployment Not Found", fmt.Sprintf("Deployment %s not found in project %s", r.PathValue("did"), r.PathValue("pid")))
			return nil
		}
		return fd
	}
	h("GET /api/projects/{pid}/deployments/{did}", func(w http.ResponseWriter, r *http.Request) {
		if fd := deployment(w, r); fd != nil {
			if strings.Contains(fd.req.ImageRef, "unreadable") && fd.gets == 0 {
				fd.gets++
				problem(w, 500, "Internal Server Error", "A database error occurred")
				return
			}
			f.advanceDeployment(fd)
			writeJSON(w, 200, deploymentJSON(fd.d))
		}
	})
	h("POST /api/projects/{pid}/deployments/{did}/cancel", func(w http.ResponseWriter, r *http.Request) {
		fd := deployment(w, r)
		if fd == nil {
			return
		}
		if strings.Contains(fd.req.ImageRef, "late") {
			f.completeDeployment(fd)
		}
		switch fd.d.Status {
		case "completed", "failed", "cancelled":
			problem(w, 400, "Deployment Cannot Be Cancelled", fmt.Sprintf("Deployment %d is already %s", fd.d.ID, fd.d.Status))
			return
		}
		fd.d.Status = "cancelled"
		fd.d.CancelledReason = ptr("Cancelled by user")
		writeJSON(w, 200, map[string]any{"id": fd.d.ID, "state": "cancelled"})
	})
}
