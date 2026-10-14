import unittest

import epoch424
from simulation import f


class InitializationTests(unittest.TestCase):
    def test_october_bounds_and_capacity(self):
        nodes, _ = epoch424.inputs()
        self.assertEqual(epoch424.PARAMS['GLM']['coeff_i_min'], 0.8352)
        self.assertEqual(epoch424.PARAMS['GLM']['coeff_i_max'], 1.48176)
        self.assertEqual(epoch424.PARAMS['DeepSeek']['coeff_i_min'], 0.3825)
        self.assertEqual(epoch424.PARAMS['DeepSeek']['coeff_i_max'], 0.569907692307)
        self.assertEqual(epoch424.PARAMS['DeepSeek']['D_i'], 1.705882352941)
        single_b300 = next(n for n in nodes if n['gpu'] == 'B300' and n['count'] == 1)
        self.assertEqual(single_b300['rates']['GLM'], 9200 / 8)

    def test_zero_target_resets_state_before_reactivation(self):
        params = {'model': dict(coeff_i_min=1, coeff_i_max=4, T_i=0)}
        state = {'model': dict(s=0.2, prev_sign=-1)}
        coeff = f({'model': 0.2}, {'model': 2}, params, 0.05, state)
        self.assertEqual(coeff['model'], 1)
        self.assertEqual(state['model'], dict(s=0.025, prev_sign=0))
        params['model']['T_i'] = 0.5
        self.assertEqual(f({'model': 0.2}, coeff, params, 0.05, state)['model'], 1.05)


if __name__ == '__main__':
    unittest.main()
