import pytest
from typing import Dict
import numpy as np
from api.inference.top_tokens import TokenLogProb, TopLogProbs, TopLogProbsSequence, compare_tokens, compare_logprobs, compare_token_sequences

# Fixtures for common test data
@pytest.fixture
def mock_json_data() -> Dict:
    return {
        'choices': [{
            'logprobs': {
                'content': [
                    {
                        'top_logprobs': [
                            {'token': 'The', 'logprob': -0.0004},
                            {'token': 'What', 'logprob': -8.5},
                            {'token': '202', 'logprob': -9.0}
                        ]
                    },
                    {
                        'top_logprobs': [
                            {'token': ' thrilling', 'logprob': -1.81},
                            {'token': ' ', 'logprob': -0.31},
                            {'token': ' thrill', 'logprob': -3.06}
                        ]
                    }
                ]
            }
        }]
    }

@pytest.fixture
def top_logprobs_pair():
    top1 = TopLogProbs()
    top1.add("hello", -1.5)
    top1.add("world", -0.8)
    top1.add("common", -2.0)
    
    top2 = TopLogProbs()
    top2.add("python", -1.2)
    top2.add("common", -1.7)
    
    return top1, top2

@pytest.fixture
def logprobs_comparison_pair():
    top1 = TopLogProbs()
    top1.add("hello", -1.5)
    top1.add("common1", -2.0)
    top1.add("common2", -0.5)
    
    top2 = TopLogProbs()
    top2.add("python", -1.2)
    top2.add("common1", -1.7)
    top2.add("common2", -0.8)
    
    return top1, top2

@pytest.fixture
def sequence_pair():
    seq1 = TopLogProbsSequence()
    
    pos1 = TopLogProbs()
    pos1.add("A", -0.1)
    pos1.add("B", -0.5)
    pos1.add("C", -1.0)
    
    pos2 = TopLogProbs()
    pos2.add("X", -0.2)
    pos2.add("Y", -0.7)
    pos2.add("Z", -1.2)
    
    seq1.add(pos1)
    seq1.add(pos2)
    
    seq2 = TopLogProbsSequence()
    
    pos1b = TopLogProbs()
    pos1b.add("A", -0.3)
    pos1b.add("B", -0.6)
    pos1b.add("C", -1.1)
    
    pos2b = TopLogProbs()
    pos2b.add("X", -0.4)
    pos2b.add("Y", -0.8)
    pos2b.add("Z", -1.3)
    
    seq2.add(pos1b)
    seq2.add(pos2b)
    
    return seq1, seq2


def test_token_logprob():
    t = TokenLogProb("hello", -1.5)
    assert t.token == "hello"
    assert t.logprob == -1.5
    assert t.to_tuple() == ("hello", -1.5)


def test_toplogprobs_add():
    top = TopLogProbs()
    top.add("hello", -1.5)
    top.add("world", -0.8)
    
    assert len(top) == 2
    assert "hello" in top.get_tokens()
    assert "world" in top.get_tokens()


def test_from_json(mock_json_data):
    sequence = TopLogProbsSequence.from_json(mock_json_data)
    
    assert len(sequence) == 2
    assert len(sequence[0]) == 3
    assert len(sequence[1]) == 3


def test_compare_tokens(top_logprobs_pair):
    top1, top2 = top_logprobs_pair
    only_in_1, only_in_2, in_both = compare_tokens(top1, top2)
    
    assert only_in_1 == {"hello", "world"}
    assert only_in_2 == {"python"}
    assert in_both == {"common"}


def test_compare_logprobs(logprobs_comparison_pair):
    top1, top2 = logprobs_comparison_pair
    comparison = compare_logprobs(top1, top2)
    
    assert "common1" in comparison
    assert "common2" in comparison
    assert np.allclose(comparison["common1"], (-2.0, -1.7, -0.3))
    assert np.allclose(comparison["common2"], (-0.5, -0.8, 0.3))


def test_compare_token_sequences(sequence_pair):
    seq1, seq2 = sequence_pair
    result = compare_token_sequences(seq1, seq2)
    assert result == [True, True]


# Adding parameterized tests
@pytest.mark.parametrize("token,logprob,expected", [
    ("hello", -1.5, ("hello", -1.5)),
    ("", 0.0, ("", 0.0)),
    ("a", -10.5, ("a", -10.5))
])
def test_token_logprob_parametrized(token, logprob, expected):
    t = TokenLogProb(token, logprob)
    assert t.to_tuple() == expected


# Test for error cases
def test_from_json_error():
    with pytest.raises(KeyError):
        TopLogProbsSequence.from_json({})