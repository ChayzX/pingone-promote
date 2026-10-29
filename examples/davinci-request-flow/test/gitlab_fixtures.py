"""GitLab settings and returned-URL fixtures: config validation and sameGitLabLocation."""
import itertools
import json

bases = ["https://gitlab.example.com", "https://gitlab.example.com/", "https://gitlab.example.com/tools", "https://gitlab.example.com/tools/",
         "https://GitLab.Example.com", "https://gitlab.example.com:8443/tools", "http://gitlab.example.com", "https://u:p@gitlab.example.com",
         "https://gitlab.example.com?x=1", "https://gitlab.example.com#f", "https://gitlab.example.com/../x", "https://gitlab.example.com/a/%2e%2e",
         "https://gitlab.example.com/a%20b", "", "   ", "gitlab.example.com", "https://"]
webs = ["https://gitlab.example.com/grp/proj", "https://GITLAB.example.com/grp/proj", "https://gitlab.example.com/tools/grp/proj",
        "https://gitlab.example.com/tools", "https://gitlab.example.com/toolsx/grp", "https://gitlab.example.com:8443/tools/grp/proj",
        "http://gitlab.example.com/grp/proj", "https://evil.example/grp/proj", "https://u@gitlab.example.com/grp", "https://gitlab.example.com/grp?x",
        "https://gitlab.example.com/grp#x", "https://gitlab.example.com/tools/%2e%2e/x", "https://gitlab.example.com/tools/../x",
        "https://gitlab.example.com/tools/a%2Fb/-/merge_requests/1", "https://gitlab.example.com/a%20b/c", "not a url", "",
        "https://gitlab.example.com.evil.example/tools/x"]
cases = []
for b, w in itertools.product(bases, webs):
    cases.append({"Base": b, "Project": "42", "Environment": "", "WebURL": w})
for p in ["42", "0", "007", "", "4a", "-1", "42/../7", "１２"]:
    cases.append({"Base": "https://gitlab.example.com", "Project": p, "Environment": "", "WebURL": "https://gitlab.example.com/x"})
for e in ["", "production", "prod-1", "prod_env", "Prod", "-prod", "prod env", "a" * 63, "a" * 64, "prod/x"]:
    cases.append({"Base": "https://gitlab.example.com", "Project": "42", "Environment": e, "WebURL": "https://gitlab.example.com/x"})
print(json.dumps(cases))
