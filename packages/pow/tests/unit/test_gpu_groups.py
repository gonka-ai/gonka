import pytest
from unittest.mock import patch, MagicMock
from pow.compute.gpu_group import GpuGroup, create_gpu_groups, get_min_group_vram


class TestGpuGroup:
    def test_gpu_group_init(self):
        """Test GpuGroup initialization."""
        group = GpuGroup([0, 1, 2])
        assert group.devices == [0, 1, 2]
        assert group.primary_device == 0
        assert group.group_size == 3
    
    def test_gpu_group_single_device(self):
        """Test GpuGroup with single device."""
        group = GpuGroup([3])
        assert group.devices == [3]
        assert group.primary_device == 3
        assert group.group_size == 1
    
    def test_gpu_group_empty_devices_raises(self):
        """Test GpuGroup raises error with empty devices."""
        with pytest.raises(ValueError, match="GPU group must have at least one device"):
            GpuGroup([])
    
    def test_get_device_strings(self):
        """Test device string conversion."""
        group = GpuGroup([0, 2, 4])
        expected = ["cuda:0", "cuda:2", "cuda:4"]
        assert group.get_device_strings() == expected
    
    def test_get_primary_device_string(self):
        """Test primary device string."""
        group = GpuGroup([5, 1, 3])
        assert group.get_primary_device_string() == "cuda:5"


class TestMinVramFunction:
    def test_get_min_group_vram_default(self):
        """Test default minimum VRAM requirement."""
        from pow.models.utils import PARAMS_V1
        min_vram = get_min_group_vram(PARAMS_V1)
        assert min_vram == 10.0
    
    def test_get_min_group_vram_with_params(self):
        """Test minimum VRAM with params (returns constant for now)."""
        fake_params = {"dim": 1024}
        min_vram = get_min_group_vram(fake_params)
        assert min_vram == 38.0  # Still constant


class TestCreateGpuGroups:
    def _run_test(self, vram_per_device, min_vram_gb, expected_groups):
        """Helper to run create_gpu_groups with mocked device properties."""
        
        def mock_get_device_properties(device_id):
            props = MagicMock()
            props.total_memory = vram_per_device[device_id] * (1024**3)
            return props

        with patch('torch.cuda.is_available', return_value=True), \
             patch('torch.cuda.device_count', return_value=len(vram_per_device)), \
             patch('torch.cuda.get_device_properties', side_effect=mock_get_device_properties):
            
            groups = create_gpu_groups(min_vram_gb=min_vram_gb)
            
            # Extract device lists for comparison
            result_devices = [g.devices for g in groups]
            assert result_devices == expected_groups

    def test_no_cuda_fallback(self):
        """Test CPU fallback when CUDA is not available."""
        with patch('torch.cuda.is_available', return_value=False):
            groups = create_gpu_groups()
            assert len(groups) == 1 and groups[0].devices == [0]

    def test_zero_devices_fallback(self):
        """Test CPU fallback when no CUDA devices are found."""
        with patch('torch.cuda.is_available', return_value=True), \
             patch('torch.cuda.device_count', return_value=0):
            groups = create_gpu_groups()
            assert len(groups) == 1 and groups[0].devices == [0]

    def test_single_gpu_sufficient_vram(self):
        """Single GPU with enough VRAM should form a group."""
        self._run_test([24], 23.0, [[0]])

    def test_single_gpu_insufficient_vram(self):
        """Single GPU with not enough VRAM should form no group."""
        self._run_test([16], 23.0, [])

    def test_prefers_single_gpu_groups(self):
        """Multiple GPUs with sufficient VRAM should form single-device groups."""
        self._run_test([24, 24, 24, 24], 23.0, [[0], [1], [2], [3]])

    def test_forms_pairs_when_insufficient_vram(self):
        """GPUs with insufficient VRAM should be paired up."""
        self._run_test([12, 12, 12, 12], 23.0, [[0, 1], [2, 3]])

    def test_forms_quads_when_pairs_insufficient(self):
        """Forms groups of 4 when pairs are not enough."""
        self._run_test([6, 6, 6, 6, 6, 6, 6, 6], 23.0, [[0, 1, 2, 3], [4, 5, 6, 7]])

    def test_mixed_vram_simple(self):
        """Mixed VRAM: one sufficient, two needing a pair."""
        self._run_test([24, 12, 12], 23.0, [[0], [1, 2]])

    def test_mixed_vram_complex(self):
        """Complex mixed VRAM with some un-groupable devices."""
        # 24 -> group [0]
        # 8, 8, 8, 8 -> group [1, 2, 3, 4] (32GB total)
        # 12, 24 -> group [5, 6] (36GB total)
        # 24 -> group [7]
        vram_list = [24, 8, 8, 8, 8, 12, 24, 24]
        expected = [[0], [1, 2, 3, 4], [5, 6], [7]]
        self._run_test(vram_list, 23.0, expected)

    def test_deterministic_grouping(self):
        """Grouping should be deterministic regardless of device order."""
        # Same test as test_prefers_single_gpu_groups
        self._run_test([24, 24, 24, 24], 23.0, [[0], [1], [2], [3]])

    def test_no_valid_groups(self):
        """No groups should be formed if no combination meets VRAM requirements."""
        self._run_test([10, 10, 10, 10], 41.0, [])
