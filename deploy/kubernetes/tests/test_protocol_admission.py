"""The protocol allowlist must not bypass dynamic router admission."""

import unittest

import yaml

from test_charts import UniqueKeyLoader, env, render


def router_settings():
    result = render({"protocols": ["v6", "v7"],
                     "router": {"activationMinReady": 2, "versionCapacity": 2}})
    if result.returncode:
        raise AssertionError(result.stderr)
    workloads = {item["metadata"]["name"]: item["spec"]["template"]["spec"]
                 for item in yaml.load_all(result.stdout, Loader=UniqueKeyLoader)
                 if item and item["kind"] in ("StatefulSet", "Deployment")}
    return {
        "router": env(workloads["test-gonka-ha-router"]["containers"][0]),
        "ingress": env(workloads["test-gonka-ha-ingress"]["initContainers"][0]),
        "oracle": env(workloads["test-gonka-ha-oracle"]["containers"][0]),
    }


class ProtocolAdmission(unittest.TestCase):
    def test_allowlist_is_not_a_static_bootstrap_exception(self):
        settings = router_settings()
        self.assertEqual(settings["oracle"]["ORACLE_ALLOW"], "v6 v7")
        for component, reserve in (("router", "VERSIOND_ROUTING_ACTIVATION_MIN_READY"),
                                   ("ingress", "PROXY_ROUTER_ACTIVATION_MIN_READY")):
            with self.subTest(component=component):
                values = settings[component]
                self.assertEqual(values["VERSIOND_VERSIONS"], "")
                self.assertEqual(values["VERSIOND_NON_HA_VERSIONS"], "")
                self.assertTrue(values["VERSIOND_ROUTING_CATALOG_URL"])
                self.assertEqual(values[reserve], "2")


if __name__ == "__main__":
    unittest.main()
