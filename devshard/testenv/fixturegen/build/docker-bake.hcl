# CPU render images for the fixture generator, built from gonka-ai/vllm sources.
#   docker buildx bake -f build/docker-bake.hcl            # all images
#   docker buildx bake -f build/docker-bake.hcl mlnode310  # one variant
# Tags match the image column of the models tables. The 3.1.0 wheel takes about 15 minutes with MAX_JOBS=8.

variable "FORK_REPO" {
  default = "https://github.com/gonka-ai/vllm.git"
}

variable "MAX_JOBS" {
  default = "8"
}

variable "IMAGE_PREFIX" {
  default = "local/"
}

# mlnode-3.1.0: release/v0.28.0-glm53.
variable "FORK_310" {
  default = "71ee49c4d66d947e3bcee46513e9148bb3946332"
}

# mlnode-3.0.16: v0.25.1-gonka-poc-0.1.3.post1.
variable "FORK_3016" {
  default = "be6059424125392e9c0d8d37f66e6f2bbdd50fb6"
}

# mlnode-4.0.0: release/v0.30-decode-int.
variable "FORK_400" {
  default = "8219a3e2e95ae706ce88a24c17092959ad138904"
}

group "default" {
  targets = ["mlnode310", "mlnode3016", "mlnode400", "mlnode400-render"]
}

# Only the wheel stage of the fork's CPU recipe: its full image does not build as is, because the
# triton-cpu stage reads TARGETARCH without declaring it. The git directory is kept so that the wheel
# version names the commit.
target "wheel310" {
  context    = "${FORK_REPO}#${FORK_310}"
  dockerfile = "docker/Dockerfile.cpu"
  target     = "vllm-build"
  platforms  = ["linux/amd64"]
  args = {
    max_jobs                      = MAX_JOBS
    BUILDKIT_CONTEXT_KEEP_GIT_DIR = "1"
  }
  output = ["type=cacheonly"]
}

target "mlnode310" {
  context    = "build"
  dockerfile = "Dockerfile.render310"
  contexts = {
    wheel = "target:wheel310"
  }
  args = {
    FORK_SHA = FORK_310
  }
  platforms = ["linux/amd64"]
  tags      = ["${IMAGE_PREFIX}vllm-cpu-glm53:71ee49c-render"]
  output    = ["type=docker"]
}

target "mlnode3016" {
  context    = "build"
  dockerfile = "Dockerfile.overlay"
  contexts = {
    fork = "${FORK_REPO}#${FORK_3016}"
  }
  args = {
    FORK_SHA = FORK_3016
  }
  platforms = ["linux/amd64"]
  tags      = ["${IMAGE_PREFIX}vllm-cpu-mlnode3016:be6059424"]
  output    = ["type=docker"]
}

target "mlnode400" {
  context    = "build"
  dockerfile = "Dockerfile.overlay400"
  contexts = {
    fork = "${FORK_REPO}#${FORK_400}"
  }
  args = {
    FORK_SHA = FORK_400
  }
  platforms = ["linux/amd64"]
  tags      = ["${IMAGE_PREFIX}vllm-cpu-mlnode400:8219a3e2e"]
  output    = ["type=docker"]
}

target "mlnode400-render" {
  context    = "build"
  dockerfile = "Dockerfile.render400"
  contexts = {
    base = "target:mlnode400"
  }
  platforms = ["linux/amd64"]
  tags      = ["${IMAGE_PREFIX}vllm-cpu-mlnode400:8219a3e2e-render"]
  output    = ["type=docker"]
}
