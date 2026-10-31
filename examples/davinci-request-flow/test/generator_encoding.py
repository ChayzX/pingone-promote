import os
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).parent.parent


class GeneratorEncodingTests(unittest.TestCase):
    def test_generated_functions_match_utf8_sources_with_cp1252_default(self):
        with tempfile.TemporaryDirectory() as temp:
            temp_path = pathlib.Path(temp)
            (temp_path / "sitecustomize.py").write_text(
                "import pathlib\n"
                "_read = pathlib.Path.read_text\n"
                "_write = pathlib.Path.write_text\n"
                "def read_text(self, *args, **kwargs):\n"
                "    kwargs.setdefault('encoding', 'cp1252')\n"
                "    return _read(self, *args, **kwargs)\n"
                "def write_text(self, *args, **kwargs):\n"
                "    kwargs.setdefault('encoding', 'cp1252')\n"
                "    return _write(self, *args, **kwargs)\n"
                "pathlib.Path.read_text = read_text\n"
                "pathlib.Path.write_text = write_text\n",
                encoding="utf-8",
            )
            output = temp_path / "export"
            values = temp_path / "values.json"
            unicode_name = "Développeur → production"
            values.write_text(json.dumps({"env1Name": unicode_name}, ensure_ascii=False), encoding="utf-8")
            env = os.environ.copy()
            env["PYTHONPATH"] = str(temp_path) + os.pathsep + env.get("PYTHONPATH", "")
            subprocess.run([sys.executable, str(HERE / "gen_flows.py"), str(output), str(values)], check=True, env=env)
            generated = json.loads((output / "pingone-promote-request.flow.json").read_text(encoding="utf-8"))
            variables = generated["flows"][0]["variables"]
            self.assertIn(unicode_name, [v["fields"]["value"] for v in variables])
            result = subprocess.run(
                ["node", str(HERE / "test" / "shipped_code.js"), str(output / "pingone-promote-request.flow.json")],
                check=False, capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()

