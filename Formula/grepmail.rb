# Hand-written template for the homebrew tap. After cutting a release with
# goreleaser, the tap repo (tajchert/homebrew-tap) will host the
# auto-generated version of this. Keep both in sync until the release flow
# is automated.
class Grepmail < Formula
  desc "Fast, grep-style CLI for searching and exploring mbox mail archives"
  homepage "https://github.com/tajchert/grepmail"
  url "https://github.com/tajchert/grepmail/archive/refs/tags/v0.2.0.tar.gz"
  sha256 "f00d59af09c7635e20562253ac88bcbb77559275f3a775bb4a1cb96a7c559720"
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
