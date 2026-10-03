"""Checks argument values used for scheduling and configuration metrics."""

import unittest

from api.inference.vllm.arg_values import get_numeric_arg_value


class TestNumericArgValue(unittest.TestCase):
    def test_last_value_matches_vllm_for_both_value_forms(self):
        args = [
            "--tensor-parallel-size", "2",
            "--tensor-parallel-size=4",
            "--max_model_len=8192",
            "--max-model-len", "240000",
        ]

        self.assertEqual(get_numeric_arg_value(args, "--tensor-parallel-size"), 4)
        self.assertEqual(get_numeric_arg_value(args, "--max-model-len"), 240000)

    def test_short_parallel_aliases_match_vllm(self):
        args = ["-tp", "2", "--tensor-parallel-size=4", "-pp=2", "--max-model=8192"]

        self.assertEqual(get_numeric_arg_value(args, "--tensor-parallel-size"), 4)
        self.assertEqual(get_numeric_arg_value(args, "--pipeline-parallel-size"), 2)
        self.assertEqual(get_numeric_arg_value(args, "--max-model-len"), 8192)

    def test_later_short_alias_and_default(self):
        args = ["--tensor_parallel_size=4", "-tp", "2"]

        self.assertEqual(get_numeric_arg_value(args, "--tensor-parallel-size"), 2)
        self.assertEqual(get_numeric_arg_value(args, "--max-num-seqs", default=0), 0)
