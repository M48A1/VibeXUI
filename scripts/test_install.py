"""Check installer helpers without installing services or writing system files."""

from pathlib import Path
import re
import subprocess
import unittest

INSTALLER = Path(__file__).with_name("install.sh").resolve()


class InstallerTests(unittest.TestCase):
    def shell(self, code):
        return subprocess.run(
            ["bash", "-c", 'source "$1"; ' + code, "bash", str(INSTALLER)],
            capture_output=True, text=True, timeout=10,
        )

    def test_sourcing_does_not_install(self):
        result = self.shell("printf loaded")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "loaded")

    def test_random_credentials_match_panel_requirements(self):
        result = self.shell(
            'generate_credentials; printf "%s %s\\n" "$username" "$password"; '
            'generate_credentials; printf "%s %s\\n" "$username" "$password"'
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertEqual(len(lines), 2)
        for line in lines:
            self.assertTrue(re.fullmatch(r"vx_[0-9a-f]{12} [0-9a-f]{48}", line))
        self.assertNotEqual(lines[0], lines[1])

    def test_random_source_failure_stops_generation(self):
        result = self.shell(
            "openssl() { return 1; }; "
            "if generate_credentials; then exit 1; fi"
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_https_checks_domain_certificate_and_panel(self):
        result = self.shell('''
curl() {
  local arg
  for arg in "$@"; do
    case "$arg" in --insecure|-k) return 1;; esac
  done
  [[ "$*" == *"--resolve panel.example.com:18443:127.0.0.1"* ]] || return 1
  [[ "${!#}" == "https://panel.example.com:18443/api/me" ]] || return 1
  printf 401
}
wait_for_https panel.example.com 18443 3
''')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_custom_port_propagates_to_config_and_login(self):
        result = self.shell('''
configure_panel_address Panel.Example.com 18443
panel_caddy_config
username=testuser password=testpass
print_login
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("https://panel.example.com:18443 {", result.stdout)
        self.assertIn("disable_tlsalpn_challenge", result.stdout)
        self.assertIn("登录地址：https://panel.example.com:18443", result.stdout)
        self.assertNotIn(":443", result.stdout)

    def test_invalid_domain_and_ports_rejected(self):
        for domain in ["https://panel.example.com", "panel.example.com:1234", "a..com", "-a.com", "a-.com", "1.2.3.4", "a.com/path"]:
            result = self.shell(f'if configure_panel_address "{domain}" 8443; then exit 1; fi')
            self.assertEqual(result.returncode, 0, result.stderr)
        for port in ["0", "65536", "443", "80", "8080", "2019", "-1", "abc", "18443/path"]:
            result = self.shell(f'if configure_panel_address panel.example.com "{port}"; then exit 1; fi')
            self.assertEqual(result.returncode, 0, result.stderr)
        result = self.shell('configure_panel_address panel.example.com 08443; printf "%s" "$panel_port"')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "8443")

    def test_invalid_certificate_never_reports_ready(self):
        result = self.shell('''
curl() { printf 401; return 60; }
sleep() { SECONDS=$((SECONDS + 100)); }
if wait_for_https panel.example.com 18443 3; then exit 1; fi
''')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_unavailable_panel_never_reports_ready(self):
        result = self.shell('''
curl() { printf 502; }
sleep() { SECONDS=$((SECONDS + 100)); }
if wait_for_panel 3; then exit 1; fi
''')
        self.assertEqual(result.returncode, 0, result.stderr)


class AgentBootstrapTests(unittest.TestCase):
    def shell(self, code):
        script = INSTALLER.parent.parent / "internal/bootstrap/agent-install.sh"
        return subprocess.run(
            ["bash", "-c", 'source "$1"; ' + code, "bash", str(script)],
            capture_output=True, text=True, timeout=10,
        )

    def test_supported_architectures_and_unknown_architecture(self):
        result = self.shell('architecture x86_64; architecture aarch64; if architecture mips; then exit 1; fi')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "amd64\narm64\n")

    def test_corrupt_download_is_rejected(self):
        result = self.shell('''
sha256sum() { printf '%064d  fixture' 0; }
if verify_sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa fixture; then exit 1; fi
if verify_sha256 invalid fixture; then exit 1; fi
''')
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
