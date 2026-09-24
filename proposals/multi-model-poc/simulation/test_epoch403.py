import copy
import unittest

import epoch403
from simulation import f


class InitializationTests(unittest.TestCase):
    def test_decode_bound_preserves_exact_terminating_value(self):
        saved = copy.deepcopy(epoch403.PARAMS)
        try:
            epoch403.inputs(decode=True)
            self.assertEqual(epoch403.PARAMS['DeepSeek']['coeff_i_max'], 0.6101088)
        finally:
            epoch403.PARAMS.clear()
            epoch403.PARAMS.update(saved)

    def test_zero_target_resets_state_before_reactivation(self):
        params = {'model': dict(coeff_i_min=1, coeff_i_max=4, T_i=0)}
        state = {'model': dict(s=0.2, prev_sign=-1)}
        coeff = f({'model': 0.2}, {'model': 2}, params, 0.05, state)
        self.assertEqual(coeff['model'], 1)
        self.assertEqual(state['model'], dict(s=0.025, prev_sign=0))

        params['model']['T_i'] = 0.5
        coeff = f({'model': 0.2}, coeff, params, 0.05, state)
        self.assertEqual(coeff['model'], 1.05)
        self.assertEqual(state['model'], dict(s=0.05, prev_sign=1))


if __name__ == '__main__':
    unittest.main()
