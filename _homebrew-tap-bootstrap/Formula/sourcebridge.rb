class Sourcebridge < Formula
  desc "Requirement-aware code comprehension platform"
  homepage "https://sourcebridge.ai"
  version "0.0.0"
  license "AGPL-3.0-only"

  # This is a placeholder formula. The release workflow in the main
  # sourcebridge repo (oss-release.yml, tap-update job) overwrites this
  # file with real URLs and SHA256s on every tagged release.

  on_macos do
    on_arm do
      url "https://github.com/sourcebridge-ai/sourcebridge/releases/download/v0.0.0/sourcebridge-v0.0.0-darwin-arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
    on_intel do
      url "https://github.com/sourcebridge-ai/sourcebridge/releases/download/v0.0.0/sourcebridge-v0.0.0-darwin-amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/sourcebridge-ai/sourcebridge/releases/download/v0.0.0/sourcebridge-v0.0.0-linux-arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
    on_intel do
      url "https://github.com/sourcebridge-ai/sourcebridge/releases/download/v0.0.0/sourcebridge-v0.0.0-linux-amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  def install
    bin.install "sourcebridge"
  end

  test do
    assert_match "sourcebridge", shell_output("#{bin}/sourcebridge --help")
  end
end
