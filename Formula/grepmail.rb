# typed: false
# frozen_string_literal: true

# Mirror of what goreleaser auto-generates in tajchert/homebrew-tap on
# each release. The canonical formula is whatever the tap repo serves at
# install time; this copy exists for reference and gets refreshed by
# `goreleaser release`. Do not hand-edit unless cutting a release
# manually — bump version, url, and sha256 for each arch.
class Grepmail < Formula
  desc "Fast, grep-style CLI for searching and exploring mbox mail archives"
  homepage "https://github.com/tajchert/grepmail"
  version "0.2.1"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/tajchert/grepmail/releases/download/v0.2.1/grepmail_0.2.1_darwin_amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"

      def install
        bin.install "grepmail"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/tajchert/grepmail/releases/download/v0.2.1/grepmail_0.2.1_darwin_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"

      def install
        bin.install "grepmail"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/tajchert/grepmail/releases/download/v0.2.1/grepmail_0.2.1_linux_amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"

      def install
        bin.install "grepmail"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/tajchert/grepmail/releases/download/v0.2.1/grepmail_0.2.1_linux_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"

      def install
        bin.install "grepmail"
      end
    end
  end

  test do
    assert_match "grepmail", shell_output("#{bin}/grepmail help")
  end
end
