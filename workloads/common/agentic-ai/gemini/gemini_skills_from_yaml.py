#!/usr/bin/env python3

# Copyright (c) 2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#         http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Converts a single skills.yaml file into Gemini CLI's expected layout:
#   <output_dir>/<skill_name>/SKILL.md
# so the CLI can discover and load skills. Called by gemini_initialise.sh during
# workspace setup (before analysis). Skills file is always named skills.yaml.
#
# Usage:
#   gemini_skills_from_yaml.py <path/to/skills.yaml> <output_dir>
#
# Arguments:
#   skills.yaml  Path to YAML with top-level "skills:" list.
#   output_dir   Directory to write into (e.g. .gemini/skills). Created if missing.
#
# YAML schema (expected under "skills:"):
#   - name: string (required, used as directory name; skip entry if empty)
#   - description: string (optional, written into SKILL.md frontmatter)
#   - system_instructions: string (optional, markdown body of SKILL.md)
#   - output_schema: string (optional, markdown template for structured model output)
#
# Output: One SKILL.md per skill with YAML frontmatter (name, description) and
# body from system_instructions. Exits 0 on success, 1 on usage/IO/PyYAML errors.
#
# Dependencies: PyYAML (pip install pyyaml, or apt install python3-yaml in images).
# Maintainer: When adding new skill fields, update this doc and any README schema.
#

import os
import sys


def main():
    """Parse skills.yaml and write one SKILL.md per skill under output_dir."""
    if len(sys.argv) != 3:
        print("Usage: gemini_skills_from_yaml.py <path/to/skills.yaml> <output_dir>", file=sys.stderr)
        sys.exit(1)
    yaml_path = sys.argv[1]
    out_dir = sys.argv[2]
    if not os.path.isfile(yaml_path):
        print(f"Error: not a file: {yaml_path}", file=sys.stderr)
        sys.exit(1)
    try:
        import yaml
    except ImportError:
        print("Error: PyYAML required. pip install pyyaml", file=sys.stderr)
        sys.exit(1)
    with open(yaml_path) as f:
        data = yaml.safe_load(f)
    skills = data.get("skills") or []
    global_constraints = data.get("global_constraints") or ""
    for s in skills:
        name = (s.get("name") or "").strip()
        if not name:
            continue
        desc = s.get("description") or ""

        schema = s.get("output_schema") or ""
        schema_block = f"\n\n## Expected Output Format\n{schema}" if schema else ""
        body = global_constraints + "\n\n" + (s.get("system_instructions") or "") + schema_block

        # Escape for YAML frontmatter double-quoted string (backslash and newline)
        desc_esc = desc.replace("\\", "\\\\").replace('"', '\\"').replace("\n", " ")
        skill_dir = os.path.join(out_dir, name)
        os.makedirs(skill_dir, exist_ok=True)
        with open(os.path.join(skill_dir, "SKILL.md"), "w") as f:
            f.write(f'---\nname: {name}\ndescription: "{desc_esc}"\n---\n\n{body}\n')
        print(f"  {name}/SKILL.md")
    return 0


if __name__ == "__main__":
    main()
