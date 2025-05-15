from dataclasses import dataclass
from typing import Tuple, Set, List, Dict, Any

@dataclass(frozen=True)
class TokenLogProb:
    token: str
    logprob: float
    
    def to_tuple(self) -> Tuple[str, float]:
        return (self.token, self.logprob)


class TopLogProbs:
    def __init__(self):
        self.items: List[TokenLogProb] = []
        
    def add(self, token: str, logprob: float) -> None:
        item = TokenLogProb(token, logprob)
        self.items.append(item)
    
    def get_tokens(self) -> Set[str]:
        return {item.token for item in self.items}
    
    def get_token_to_logprob_dict(self) -> dict:
        return {item.token: item.logprob for item in self.items}
    
    def __len__(self) -> int:
        return len(self.items)
    
    def __iter__(self):
        return iter(self.items)


class TopLogProbsSequence:
    def __init__(self):
        self.sequence: List[TopLogProbs] = []
    
    def add(self, top_logprobs: TopLogProbs) -> None:
        self.sequence.append(top_logprobs)
    
    @classmethod
    def from_json(cls, json_data: Dict[str, Any]) -> 'TopLogProbsSequence':
        result = cls()
        content_logprobs = json_data['choices'][0]['logprobs']['content']
        
        for token_json in content_logprobs:
            position_logprobs = TopLogProbs()
            
            for top_logprob in token_json['top_logprobs']:
                position_logprobs.add(top_logprob['token'], top_logprob['logprob'])
            
            result.add(position_logprobs)
            
        return result
    
    def __len__(self) -> int:
        return len(self.sequence)
    
    def __getitem__(self, index: int) -> TopLogProbs:
        return self.sequence[index]


def compare_tokens(top_logprobs_1: TopLogProbs, top_logprobs_2: TopLogProbs) -> Tuple[Set[str], Set[str], Set[str]]:
    tokens_1 = top_logprobs_1.get_tokens()
    tokens_2 = top_logprobs_2.get_tokens()
    
    only_in_1 = tokens_1 - tokens_2
    only_in_2 = tokens_2 - tokens_1
    in_both = tokens_1 & tokens_2
    
    return (only_in_1, only_in_2, in_both)


def compare_logprobs(top_logprobs_1: TopLogProbs, top_logprobs_2: TopLogProbs) -> dict:
    dict_1 = top_logprobs_1.get_token_to_logprob_dict()
    dict_2 = top_logprobs_2.get_token_to_logprob_dict()
    
    _, _, common_tokens = compare_tokens(top_logprobs_1, top_logprobs_2)
    
    result = {}
    for token in common_tokens:
        logprob_1 = dict_1[token]
        logprob_2 = dict_2[token]
        difference = logprob_1 - logprob_2
        result[token] = (logprob_1, logprob_2, difference)
        
    return result


def compare_token_sequences(seq_1: TopLogProbsSequence, seq_2: TopLogProbsSequence) -> List[bool]:
    min_length = min(len(seq_1), len(seq_2))
    result = []
    
    for i in range(min_length):
        logprobs_1 = sorted(seq_1[i].items, key=lambda x: x.logprob, reverse=True)
        logprobs_2 = sorted(seq_2[i].items, key=lambda x: x.logprob, reverse=True)
        
        tokens_1 = [item.token for item in logprobs_1]
        tokens_2 = [item.token for item in logprobs_2]
        
        min_tokens = min(len(tokens_1), len(tokens_2))
        tokens_match = tokens_1[:min_tokens] == tokens_2[:min_tokens]
        
        result.append(tokens_match)
    
    return result