import numpy as np


def estimate_R_from_experiment(n, P, num_samples=1000000):
    points = np.random.normal(size=(num_samples, n))
    points /= np.linalg.norm(points, axis=1)[:, np.newaxis]

    sample_point = points[0]
    distances = np.linalg.norm(points - sample_point, axis=1)

    sorted_distances = np.sort(distances)

    index = int(P * num_samples)
    R = sorted_distances[index]

    return R
