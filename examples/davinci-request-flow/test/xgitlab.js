// JS side of the GitLab rule cross-check (cmd/xreq with a third argument).
const core = require('./load_core');
const fs = require('fs');

const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const out = fixtures.map((f) => {
  let cfg;
  try { cfg = core.gitlabConfig(f.Base, f.Project, f.Environment); } catch (e) { return { configError: true, same: false }; }
  return { configError: false, same: core.sameGitLabLocation(cfg.base, f.WebURL) };
});
fs.writeFileSync(process.argv[3], JSON.stringify(out));
console.log('js checked ' + out.length + ' GitLab fixtures');
