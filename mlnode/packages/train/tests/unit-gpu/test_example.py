import torch

def test_gpu_operation():
    device = torch.device('cuda' if torch.cuda.is_available() else 'cpu')
    
    x = torch.tensor([1.0, 2.0, 3.0]).to(device)
    y = torch.tensor([4.0, 5.0, 6.0]).to(device)
    
    result = x + y
    
    expected = torch.tensor([5.0, 7.0, 9.0])
    torch.testing.assert_close(result.cpu(), expected)