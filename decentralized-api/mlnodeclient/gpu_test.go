package mlnodeclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetGPUDevices(t *testing.T) {
	ctx := context.Background()

	t.Run("successful response", func(t *testing.T) {
		expectedResp := &GPUDevicesResponse{
			Devices: []GPUDevice{
				{
					Index:       0,
					Name:        "NVIDIA A100-SXM4-40GB",
					IsAvailable: true,
				},
			},
			Count: 1,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/gpu/devices" {
				t.Errorf("expected path /api/v1/gpu/devices, got %s", r.URL.Path)
			}
			if r.Method != http.MethodGet {
				t.Errorf("expected GET method, got %s", r.Method)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.GetGPUDevices(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Count != 1 {
			t.Errorf("expected count 1, got %d", resp.Count)
		}
		if len(resp.Devices) != 1 {
			t.Errorf("expected 1 device, got %d", len(resp.Devices))
		}
		if resp.Devices[0].Name != "NVIDIA A100-SXM4-40GB" {
			t.Errorf("expected device name NVIDIA A100-SXM4-40GB, got %s", resp.Devices[0].Name)
		}
	})

	t.Run("empty devices list", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(&GPUDevicesResponse{
				Devices: []GPUDevice{},
				Count:   0,
			})
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.GetGPUDevices(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Count != 0 {
			t.Errorf("expected count 0, got %d", resp.Count)
		}
	})

	t.Run("API not implemented - 404", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.GetGPUDevices(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var apiErr *ErrAPINotImplemented
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T: %v", err, err)
		} else {
			apiErr = err.(*ErrAPINotImplemented)
			if apiErr.StatusCode != http.StatusNotFound {
				t.Errorf("expected status code 404, got %d", apiErr.StatusCode)
			}
		}
	})

	t.Run("API not implemented - 405", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.GetGPUDevices(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T: %v", err, err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.GetGPUDevices(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if isErrAPINotImplemented(err) {
			t.Error("expected generic error, got ErrAPINotImplemented")
		}
	})
}

func TestClient_GetGPUDriver(t *testing.T) {
	ctx := context.Background()

	t.Run("successful response", func(t *testing.T) {
		expectedResp := &DriverInfo{
			DriverVersion:     "535.104.05",
			CudaDriverVersion: "12.2",
			NvmlVersion:       "12.535.104",
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/gpu/driver" {
				t.Errorf("expected path /api/v1/gpu/driver, got %s", r.URL.Path)
			}
			if r.Method != http.MethodGet {
				t.Errorf("expected GET method, got %s", r.Method)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(expectedResp)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		resp, err := client.GetGPUDriver(ctx)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.DriverVersion != "535.104.05" {
			t.Errorf("expected driver version 535.104.05, got %s", resp.DriverVersion)
		}
		if resp.CudaDriverVersion != "12.2" {
			t.Errorf("expected CUDA version 12.2, got %s", resp.CudaDriverVersion)
		}
	})

	t.Run("API not implemented", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.GetGPUDriver(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !isErrAPINotImplemented(err) {
			t.Errorf("expected ErrAPINotImplemented, got %T: %v", err, err)
		}
	})

	t.Run("service unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		client := NewNodeClient(server.URL, "", "", "")
		_, err := client.GetGPUDriver(ctx)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// Helper function to check if error is ErrAPINotImplemented
func isErrAPINotImplemented(err error) bool {
	_, ok := err.(*ErrAPINotImplemented)
	return ok
}
