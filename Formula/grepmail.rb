# Hand-written template for the homebrew tap. After cutting a release with
# goreleaser, the tap repo (tajchert/homebrew-tap) will host the
# auto-generated version of this. Keep both in sync until the release flow
# is automated.
class Grepmail < Formula
  desc "Fast, grep-style CLI for searching and exploring mbox mail archives"
  homepage "https://github.com/tajchert/grepmail"
  url "https://github.com/tajchert/grepmail/archive/refs/tags/v0.2.1.tar.gz"
  sha256 "7c78f28c4d0669439953a05492bad5865b8a02b65b07a76576022b6eb2f4bbfd"
  license "MIT"
  head "https://github.com/tajchert/grepmail.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X main.version=#{version}"
    system "go", "build", *std_go_args(ldflags: ldflags), "./cmd/grepmail"
  end

  test do
    assert_match "grepmail", shell_output("#{bin}/grepmail help")
  end
end
