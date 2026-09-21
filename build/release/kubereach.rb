cask "kubereach" do
  version "${VERSION}"
  sha256 "${SHA256_DARWIN}"

  url "https://github.com/edereagzi/kubereach/releases/download/v#{version}/kubereach_darwin_universal.zip"
  name "Kubereach"
  desc "Access Kubernetes clusters behind SSH bastions, VPNs and closed networks"
  homepage "https://github.com/edereagzi/kubereach"

  depends_on macos: ">= :monterey"

  app "Kubereach.app"

  # Builds are unsigned; drop the quarantine flag Homebrew sets so Gatekeeper lets the app open.
  postflight do
    system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{appdir}/Kubereach.app"]
  end

  zap trash: "~/Library/Application Support/kubereach"
end
