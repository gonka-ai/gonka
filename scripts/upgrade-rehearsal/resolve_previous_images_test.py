#!/usr/bin/env python3
"""Check how resolve_previous_images.py reads image references from a join compose file."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))

import resolve_previous_images as rpi  # noqa: E402

SCRIPT = Path(__file__).resolve().parent / "resolve_previous_images.py"

# The shapes deploy/join/docker-compose.yml uses in release/v0.2.16: folded scalars
# for proxy, versiond and the proxy policy anchor, plain values for node and api,
# and proxy-policy taking its nginx image from the anchor.
COMPOSE = """\
x-proxy-policy: &proxy-policy
  image: >-
    ${PROXY_POLICY_IMAGE:-ghcr.io/product-science/proxy:0.2.15-devshard-v5}
  environment:
    - NGINX_MODE=${NGINX_MODE:-http}

services:
  node:
    container_name: node
    image: ghcr.io/product-science/inferenced:0.2.15
    command: ["sh", "./init-docker.sh"]

  api:
    container_name: api
    image: ghcr.io/product-science/api:0.2.15-post3

  versiond:
    container_name: versiond
    image: >-
      ${VERSIOND_IMAGE:-ghcr.io/product-science/versiond:0.2.15-devshard-v5}
    environment:
      - VERSIOND_ORACLE_URL=http://api:9100/versions

  proxy-policy:
    <<: *proxy-policy

  proxy:
    container_name: proxy
    image: >-
      ${PROXY_ROUTER_IMAGE:-ghcr.io/product-science/proxy-router:0.2.15-devshard-v5}
    ports:
      - "${API_PORT:-8000}:80"
"""


class ResolvePreviousImages(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="resolve-previous-images-")
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)

    def write_compose(self, text):
        path = self.dir / "docker-compose.yml"
        path.write_text(text)
        return path

    def test_folded_scalar_reads_the_next_line(self):
        images = rpi.parse_service_images(self.write_compose(COMPOSE))
        self.assertEqual(images["node"], "ghcr.io/product-science/inferenced:0.2.15")
        self.assertEqual(images["api"], "ghcr.io/product-science/api:0.2.15-post3")
        self.assertEqual(images["versiond"], "${VERSIOND_IMAGE:-ghcr.io/product-science/versiond:0.2.15-devshard-v5}")
        self.assertEqual(images["proxy"], "${PROXY_ROUTER_IMAGE:-ghcr.io/product-science/proxy-router:0.2.15-devshard-v5}")
        self.assertEqual(images["proxy-policy"], "${PROXY_POLICY_IMAGE:-ghcr.io/product-science/proxy:0.2.15-devshard-v5}")

    def test_service_image_wins_over_the_anchor(self):
        text = COMPOSE.replace("    <<: *proxy-policy\n", "    <<: *proxy-policy\n    image: ghcr.io/example/proxy:2\n")
        images = rpi.parse_service_images(self.write_compose(text))
        self.assertEqual(images["proxy-policy"], "ghcr.io/example/proxy:2")

    def test_other_block_scalar_headers(self):
        for header in (">", ">-", ">+", "|", "|-", "|+"):
            with self.subTest(header=header):
                text = f"services:\n  node:\n    image: {header}\n      ghcr.io/example/node:1\n    restart: always\n"
                images = rpi.parse_service_images(self.write_compose(text))
                self.assertEqual(images["node"], "ghcr.io/example/node:1")

    def test_default_when_the_variable_is_unset(self):
        value = "${VERSIOND_IMAGE:-ghcr.io/product-science/versiond:0.2.15-devshard-v5}"
        self.assertEqual(rpi.resolve_image("versiond", value, {}), "ghcr.io/product-science/versiond:0.2.15-devshard-v5")

    def test_the_variable_wins_over_the_default(self):
        value = "${VERSIOND_IMAGE:-ghcr.io/product-science/versiond:0.2.15-devshard-v5}"
        environ = {"VERSIOND_IMAGE": "ghcr.io/example/versiond:2"}
        self.assertEqual(rpi.resolve_image("versiond", value, environ), "ghcr.io/example/versiond:2")

    def test_empty_variable_follows_compose_rules(self):
        environ = {"IMAGE": ""}
        # ${VAR:-default} also replaces an empty value; ${VAR-default} only an unset one.
        self.assertEqual(rpi.resolve_image("node", "${IMAGE:-ghcr.io/example/node:1}", environ), "ghcr.io/example/node:1")
        with self.assertRaisesRegex(RuntimeError, "resolves to ''"):
            rpi.resolve_image("node", "${IMAGE-ghcr.io/example/node:1}", environ)
        self.assertEqual(rpi.resolve_image("node", "${IMAGE-ghcr.io/example/node:1}", {}), "ghcr.io/example/node:1")

    def test_unset_variable_without_default_is_an_error(self):
        with self.assertRaisesRegex(RuntimeError, r"\$\{IMAGE\}, which is not set"):
            rpi.resolve_image("node", "${IMAGE}", {})
        self.assertEqual(rpi.resolve_image("node", "ghcr.io/example/node:${TAG}", {"TAG": "3"}), "ghcr.io/example/node:3")

    def test_quotes_are_removed(self):
        self.assertEqual(rpi.resolve_image("api", '"ghcr.io/example/api:1"', {}), "ghcr.io/example/api:1")
        self.assertEqual(rpi.resolve_image("api", "'ghcr.io/example/api:1'", {}), "ghcr.io/example/api:1")

    def test_unreadable_values_are_errors(self):
        # ">-" is what the old parser passed on to docker pull.
        for value in (">-", "", "${IMAGE:?set IMAGE}", "two words"):
            with self.subTest(value=value):
                with self.assertRaisesRegex(RuntimeError, "cannot read the image"):
                    rpi.resolve_image("proxy", value, {})
        text = "services:\n  proxy:\n    image: >-\n    ports:\n      - \"80:80\"\n"
        images = rpi.parse_service_images(self.write_compose(text))
        with self.assertRaisesRegex(RuntimeError, "cannot read the image"):
            rpi.resolve_image("proxy", images["proxy"], {})

    def test_script_outputs(self):
        compose = self.write_compose(COMPOSE)
        output = self.dir / "github_output"
        env = {key: value for key, value in os.environ.items()
               if key not in ("VERSIOND_IMAGE", "PROXY_ROUTER_IMAGE", "PROXY_POLICY_IMAGE")}
        env["GITHUB_OUTPUT"] = str(output)
        subprocess.run([sys.executable, str(SCRIPT), "--compose", str(compose)],
                       check=True, capture_output=True, text=True, env=env)
        outputs = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(outputs["node_image"], "ghcr.io/product-science/inferenced:0.2.15")
        self.assertEqual(outputs["api_image"], "ghcr.io/product-science/api:0.2.15-post3")
        # Local Testermint runs ghcr.io/product-science/proxy:latest as its nginx proxy, so it
        # takes the proxy-policy image, not the proxy-router one.
        self.assertEqual(outputs["proxy_image"], "ghcr.io/product-science/proxy:0.2.15-devshard-v5")
        self.assertEqual(outputs["proxy_target_image"], "ghcr.io/product-science/proxy:latest")
        self.assertEqual(outputs["versiond_image"], "ghcr.io/product-science/versiond:0.2.15-devshard-v5")
        self.assertNotIn(">-", output.read_text())

    def test_proxy_service_without_proxy_policy(self):
        # Up to v0.2.15 the `proxy` service itself ran the nginx image.
        text = ("services:\n  node:\n    image: ghcr.io/product-science/inferenced:0.2.15\n"
                "  api:\n    image: ghcr.io/product-science/api:0.2.15\n"
                "  proxy:\n    image: ghcr.io/product-science/proxy:0.2.15\n")
        output = self.dir / "github_output"
        env = dict(os.environ, GITHUB_OUTPUT=str(output))
        subprocess.run([sys.executable, str(SCRIPT), "--compose", str(self.write_compose(text))],
                       check=True, capture_output=True, text=True, env=env)
        outputs = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(outputs["proxy_image"], "ghcr.io/product-science/proxy:0.2.15")
        self.assertEqual(outputs["versiond_image"], "")


if __name__ == "__main__":
    unittest.main()
