"""Integration tests for PoC v2 routes (fan-out, status-aware LB, composite request_id)."""
import pytest
from unittest.mock import patch, MagicMock, AsyncMock
from fastapi.testclient import TestClient

from api.app import app


@pytest.fixture
def client():
    return TestClient(app)


def make_mock_response(status_code: int, json_data: dict):
    """Create a mock httpx response."""
    mock = MagicMock()
    mock.status_code = status_code
    mock.json.return_value = json_data
    mock.text = str(json_data)
    return mock


class TestInitGenerateFanout:
    """Test fan-out /init/generate with group_id injection."""
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002, 5003])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True, 5003: True})
    @patch('api.proxy.vllm_counts', {5001: 0, 5002: 0, 5003: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "IDLE", 5002: "IDLE", 5003: "IDLE"})
    def test_fanout_injects_group_ids(self, client):
        """Test that /init/generate fans out to all backends with correct group_id."""
        captured_calls = []
        
        async def mock_post(url, json=None, timeout=None):
            captured_calls.append({"url": url, "json": json})
            return make_mock_response(200, {"status": "OK", "pow_status": {"status": "GENERATING"}})
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            
            response = client.post("/api/v1/inference/pow/init/generate", json={
                "block_hash": "0xabc123",
                "block_height": 100,
                "public_key": "pub_key_test",
                "node_id": 0,
                "node_count": 10,
                "batch_size": 32,
                "params": {"model": "test-model", "seq_len": 256, "k_dim": 12}
            })
            
            assert response.status_code == 200
            data = response.json()
            assert data["status"] == "OK"
            assert data["backends"] == 3
            assert data["n_groups"] == 3
            
            # Verify group_id injection
            assert len(captured_calls) == 3
            group_ids = sorted([c["json"]["group_id"] for c in captured_calls])
            n_groups_values = [c["json"]["n_groups"] for c in captured_calls]
            
            assert group_ids == [0, 1, 2]
            assert all(n == 3 for n in n_groups_values)
    
    @patch('api.proxy.vllm_backend_ports', [])
    @patch('api.proxy.vllm_healthy', {})
    def test_fanout_no_backends(self, client):
        """Test /init/generate returns 503 when no backends available."""
        response = client.post("/api/v1/inference/pow/init/generate", json={
            "block_hash": "0xabc123",
            "block_height": 100,
            "public_key": "pub_key_test",
            "node_id": 0,
            "node_count": 10,
            "params": {"model": "test-model", "seq_len": 256}
        })
        
        assert response.status_code == 503


class TestRoundRobinLoadBalancing:
    """Test that /generate uses round-robin across healthy backends."""
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
    @patch('api.proxy.pow_generate_rr_index', 0)
    def test_round_robin_cycles_backends(self, client):
        """Test /generate cycles through backends in round-robin order."""
        captured_urls = []
        
        async def mock_post(url, json=None, timeout=None):
            captured_urls.append(url)
            return make_mock_response(200, {
                "status": "completed",
                "request_id": "test-uuid",
                "artifacts": []
            })
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            
            request_body = {
                "block_hash": "0xabc123",
                "block_height": 100,
                "public_key": "pub_key_test",
                "node_id": 0,
                "node_count": 10,
                "nonces": [0, 1, 2],
                "params": {"model": "test-model", "seq_len": 256},
                "wait": True
            }
            
            # Make 4 requests to see round-robin cycling
            for _ in range(4):
                response = client.post("/api/v1/inference/pow/generate", json=request_body)
                assert response.status_code == 200
            
            # Should cycle: 5001, 5002, 5001, 5002
            assert len(captured_urls) == 4
            assert "5001" in captured_urls[0]
            assert "5002" in captured_urls[1]
            assert "5001" in captured_urls[2]
            assert "5002" in captured_urls[3]
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002, 5003])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: False, 5003: True})
    @patch('api.proxy.pow_generate_rr_index', 0)
    def test_round_robin_skips_unhealthy(self, client):
        """Test /generate skips unhealthy backends in round-robin."""
        captured_urls = []
        
        async def mock_post(url, json=None, timeout=None):
            captured_urls.append(url)
            return make_mock_response(200, {
                "status": "completed",
                "request_id": "test-uuid",
                "artifacts": []
            })
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            
            request_body = {
                "block_hash": "0xabc123",
                "block_height": 100,
                "public_key": "pub_key_test",
                "node_id": 0,
                "node_count": 10,
                "nonces": [0, 1, 2],
                "params": {"model": "test-model", "seq_len": 256},
                "wait": True
            }
            
            # Make 4 requests
            for _ in range(4):
                response = client.post("/api/v1/inference/pow/generate", json=request_body)
                assert response.status_code == 200
            
            # Should cycle only healthy: 5001, 5003, 5001, 5003 (5002 skipped)
            assert len(captured_urls) == 4
            for url in captured_urls:
                assert "5002" not in url


class TestQueuedRequestIdRoundtrip:
    """Test composite request_id for queued /generate requests."""
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
    @patch('api.proxy.vllm_counts', {5001: 0, 5002: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "IDLE", 5002: "IDLE"})
    def test_queued_returns_composite_request_id(self, client):
        """Test that wait=false returns composite request_id with port prefix."""
        async def mock_post(url, json=None, timeout=None):
            return make_mock_response(200, {
                "status": "queued",
                "request_id": "backend-uuid-123",
                "queued_count": 3
            })
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            
            response = client.post("/api/v1/inference/pow/generate", json={
                "block_hash": "0xabc123",
                "block_height": 100,
                "public_key": "pub_key_test",
                "node_id": 0,
                "node_count": 10,
                "nonces": [0, 1, 2],
                "params": {"model": "test-model", "seq_len": 256},
                "wait": False
            })
            
            assert response.status_code == 200
            data = response.json()
            assert data["status"] == "queued"
            # request_id should be composite: "{port}:{backend_uuid}"
            assert ":" in data["request_id"]
            port, backend_id = data["request_id"].split(":", 1)
            assert port in ["5001", "5002"]
            assert backend_id == "backend-uuid-123"
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
    @patch('api.proxy.vllm_counts', {5001: 0, 5002: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "IDLE", 5002: "IDLE"})
    def test_poll_routes_to_correct_backend(self, client):
        """Test GET /generate/{request_id} routes to correct backend based on port prefix."""
        captured_url = []
        
        async def mock_get(url, timeout=None):
            captured_url.append(url)
            return make_mock_response(200, {
                "status": "completed",
                "request_id": "backend-uuid-123",
                "artifacts": [{"nonce": 0, "vector_b64": "AAAA"}]
            })
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.get = AsyncMock(side_effect=mock_get)
            
            # Poll with composite request_id
            response = client.get("/api/v1/inference/pow/generate/5002:backend-uuid-123")
            
            assert response.status_code == 200
            data = response.json()
            assert data["status"] == "completed"
            # Composite request_id preserved in response
            assert data["request_id"] == "5002:backend-uuid-123"
            
            # Verify it routed to port 5002
            assert len(captured_url) == 1
            assert "5002" in captured_url[0]
            assert "backend-uuid-123" in captured_url[0]
    
    def test_poll_invalid_request_id_format(self, client):
        """Test GET /generate returns 400 for invalid request_id format."""
        response = client.get("/api/v1/inference/pow/generate/invalid-no-colon")
        assert response.status_code == 400
        assert "Invalid request_id format" in response.json()["detail"]
    
    def test_poll_invalid_port_in_request_id(self, client):
        """Test GET /generate returns 400 for non-numeric port in request_id."""
        response = client.get("/api/v1/inference/pow/generate/notaport:uuid")
        assert response.status_code == 400
        assert "Invalid port" in response.json()["detail"]


class TestStopFanout:
    """Test /stop fans out to all backends."""
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
    @patch('api.proxy.vllm_counts', {5001: 0, 5002: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "GENERATING", 5002: "GENERATING"})
    def test_stop_calls_all_backends(self, client):
        """Test /stop calls all healthy backends."""
        captured_urls = []
        
        async def mock_post(url, json=None, timeout=None):
            captured_urls.append(url)
            return make_mock_response(200, {"status": "OK", "pow_status": {"status": "STOPPED"}})
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            
            response = client.post("/api/v1/inference/pow/stop")
            
            assert response.status_code == 200
            data = response.json()
            assert data["status"] == "OK"
            assert len(data["results"]) == 2
            
            # Verify both backends were called
            assert len(captured_urls) == 2
            ports_called = {"5001" in u or "5002" in u for u in captured_urls}
            assert True in ports_called


class TestStatusAggregation:
    """Test /status aggregates from all backends."""
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
    @patch('api.proxy.vllm_counts', {5001: 0, 5002: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "GENERATING", 5002: "IDLE"})
    def test_status_mixed(self, client):
        """Test /status returns MIXED when backends have different states."""
        call_count = [0]
        
        async def mock_get(url, timeout=None):
            call_count[0] += 1
            if "5001" in url:
                return make_mock_response(200, {"status": "GENERATING", "stats": {"total_processed": 100}})
            else:
                return make_mock_response(200, {"status": "IDLE"})
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.get = AsyncMock(side_effect=mock_get)
            
            response = client.get("/api/v1/inference/pow/status")
            
            assert response.status_code == 200
            data = response.json()
            assert data["status"] == "MIXED"
            assert len(data["backends"]) == 2
    
    @patch('api.proxy.vllm_backend_ports', [5001, 5002])
    @patch('api.proxy.vllm_healthy', {5001: True, 5002: True})
    @patch('api.proxy.vllm_counts', {5001: 0, 5002: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "GENERATING", 5002: "GENERATING"})
    def test_status_all_generating(self, client):
        """Test /status returns GENERATING when all backends are generating."""
        async def mock_get(url, timeout=None):
            return make_mock_response(200, {"status": "GENERATING", "stats": {"total_processed": 100}})
        
        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.get = AsyncMock(side_effect=mock_get)
            
            response = client.get("/api/v1/inference/pow/status")
            
            assert response.status_code == 200
            data = response.json()
            assert data["status"] == "GENERATING"
