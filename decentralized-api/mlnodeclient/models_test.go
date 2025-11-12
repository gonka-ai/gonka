package mlnodeclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_CheckModelStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("model downloaded", func(t *testing.T) {
		model := Model{
			HfRepo:   "meta-llama/Llama-2-7b-hf",
			HfCommit: nil,
		}

		expectedResp := &ModelStatusResponse{
			Model:  model,
			Status: ModelStatusDownloaded,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/models/status" {
				t.Errorf("expected path /api/v1/models/status, got %s", r.URL.Path)
			}
			if r.Method != http.MethodPost {
				t.Errorf("expected POST method, got %s", r.Method)
			}

			// Verify request body
			body, _ := io.ReadAll(r.Body)
			var reqModel Model
			json.Unmarshal(body, &reqModel)
			if reqModel.HfRepo != model.HfRepo {
				t.Errorf("expected repo %s, got %s", model.HfRepo, reqModel.HfRepo)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.CheckModelStatus(ctx, model)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != ModelStatusDownloaded {
			t.Errorf("expected status DOWNLOADED, got %s", resp.Status)
		}
	})

	t.Run("model downloading with progress", func(t *testing.T) {
		model := Model{
			HfRepo:   "meta-llama/Llama-2-7b-hf",
			HfCommit: nil,
		}

		expectedResp := &ModelStatusResponse{
			Model:  model,
			Status: ModelStatusDownloading,
			Progress: &DownloadProgress{
				StartTime:      1728565234.123,
				ElapsedSeconds: 125.5,
			},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.CheckModelStatus(ctx, model)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != ModelStatusDownloading {
			t.Errorf("expected status DOWNLOADING, got %s", resp.Status)
		}
		if resp.Progress == nil {
			t.Fatal("expected progress info, got nil")
		}
		if resp.Progress.ElapsedSeconds != 125.5 {
			t.Errorf("expected elapsed 125.5, got %f", resp.Progress.ElapsedSeconds)
		}
	})

	t.Run("model not found", func(t *testing.T) {
		model := Model{
			HfRepo:   "unknown/model",
			HfCommit: nil,
		}

		expectedResp := &ModelStatusResponse{
			Model:  model,
			Status: ModelStatusNotFound,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.CheckModelStatus(ctx, model)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != ModelStatusNotFound {
			t.Errorf("expected status NOT_FOUND, got %s", resp.Status)
		}
	})

	t.Run("API not implemented", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		model := Model{HfRepo: "test/model"}
		_, err := client.CheckModelStatus(ctx, model)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T", err)
		}
	})
}

func TestClient_DownloadModel(t *testing.T) {
	ctx := context.Background()

	t.Run("successful download start", func(t *testing.T) {
		model := Model{
			HfRepo:   "meta-llama/Llama-2-7b-hf",
			HfCommit: nil,
		}

		expectedResp := &DownloadStartResponse{
			TaskId: "meta-llama/Llama-2-7b-hf:latest",
			Status: ModelStatusDownloading,
			Model:  model,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/models/download" {
				t.Errorf("expected path /api/v1/models/download, got %s", r.URL.Path)
			}
			if r.Method != http.MethodPost {
				t.Errorf("expected POST method, got %s", r.Method)
			}

			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.DownloadModel(ctx, model)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != ModelStatusDownloading {
			t.Errorf("expected status DOWNLOADING, got %s", resp.Status)
		}
		if resp.TaskId != "meta-llama/Llama-2-7b-hf:latest" {
			t.Errorf("expected task_id meta-llama/Llama-2-7b-hf:latest, got %s", resp.TaskId)
		}
	})

	t.Run("already downloading - conflict", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		model := Model{HfRepo: "test/model"}
		_, err := client.DownloadModel(ctx, model)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "model is already downloading" {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("too many requests", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		model := Model{HfRepo: "test/model"}
		_, err := client.DownloadModel(ctx, model)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "maximum concurrent downloads reached" {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("API not implemented", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		model := Model{HfRepo: "test/model"}
		_, err := client.DownloadModel(ctx, model)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T", err)
		}
	})
}

func TestClient_DeleteModel(t *testing.T) {
	ctx := context.Background()

	t.Run("successful deletion", func(t *testing.T) {
		model := Model{
			HfRepo:   "meta-llama/Llama-2-7b-hf",
			HfCommit: nil,
		}

		expectedResp := &DeleteResponse{
			Status: "deleted",
			Model:  model,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/models" {
				t.Errorf("expected path /api/v1/models, got %s", r.URL.Path)
			}
			if r.Method != http.MethodDelete {
				t.Errorf("expected DELETE method, got %s", r.Method)
			}

			// Verify request body
			body, _ := io.ReadAll(r.Body)
			var reqModel Model
			json.Unmarshal(body, &reqModel)
			if reqModel.HfRepo != model.HfRepo {
				t.Errorf("expected repo %s, got %s", model.HfRepo, reqModel.HfRepo)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.DeleteModel(ctx, model)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != "deleted" {
			t.Errorf("expected status deleted, got %s", resp.Status)
		}
	})

	t.Run("cancelled download", func(t *testing.T) {
		model := Model{
			HfRepo:   "meta-llama/Llama-2-7b-hf",
			HfCommit: nil,
		}

		expectedResp := &DeleteResponse{
			Status: "cancelled",
			Model:  model,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.DeleteModel(ctx, model)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Status != "cancelled" {
			t.Errorf("expected status cancelled, got %s", resp.Status)
		}
	})

	t.Run("API not implemented", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		model := Model{HfRepo: "test/model"}
		_, err := client.DeleteModel(ctx, model)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T", err)
		}
	})
}

func TestClient_ListModels(t *testing.T) {
	ctx := context.Background()

	t.Run("successful list with models", func(t *testing.T) {
		commit1 := "abc123"
		expectedResp := &ModelListResponse{
			Models: []ModelListItem{
				{
					Model: Model{
						HfRepo:   "meta-llama/Llama-2-7b-hf",
						HfCommit: &commit1,
					},
					Status: ModelStatusDownloaded,
				},
				{
					Model: Model{
						HfRepo:   "microsoft/phi-2",
						HfCommit: nil,
					},
					Status: ModelStatusPartial,
				},
			},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/models/list" {
				t.Errorf("expected path /api/v1/models/list, got %s", r.URL.Path)
			}
			if r.Method != http.MethodGet {
				t.Errorf("expected GET method, got %s", r.Method)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.ListModels(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Models) != 2 {
			t.Errorf("expected 2 models, got %d", len(resp.Models))
		}
		if resp.Models[0].Status != ModelStatusDownloaded {
			t.Errorf("expected first model status DOWNLOADED, got %s", resp.Models[0].Status)
		}
	})

	t.Run("empty list", func(t *testing.T) {
		expectedResp := &ModelListResponse{
			Models: []ModelListItem{},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.ListModels(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Models) != 0 {
			t.Errorf("expected 0 models, got %d", len(resp.Models))
		}
	})

	t.Run("API not implemented", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.ListModels(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T", err)
		}
	})
}

func TestClient_GetDiskSpace(t *testing.T) {
	ctx := context.Background()

	t.Run("successful response", func(t *testing.T) {
		expectedResp := &DiskSpaceInfo{
			CacheSizeGB: 13.0,
			AvailableGB: 465.66,
			CachePath:   "/root/.cache/hub",
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/models/space" {
				t.Errorf("expected path /api/v1/models/space, got %s", r.URL.Path)
			}
			if r.Method != http.MethodGet {
				t.Errorf("expected GET method, got %s", r.Method)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.GetDiskSpace(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.CacheSizeGB != 13.0 {
			t.Errorf("expected cache size 13.0, got %f", resp.CacheSizeGB)
		}
		if resp.AvailableGB != 465.66 {
			t.Errorf("expected available 465.66, got %f", resp.AvailableGB)
		}
		if resp.CachePath != "/root/.cache/hub" {
			t.Errorf("expected cache path /root/.cache/hub, got %s", resp.CachePath)
		}
	})

	t.Run("API not implemented", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.GetDiskSpace(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T", err)
		}
	})
}
