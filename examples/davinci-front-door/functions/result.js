module.exports = a = async ({params}) => {
  // Confirm GitLab created both the commit and the merge request.
  let mr = params.mrBody;
  if (typeof mr === 'string') { try { mr = JSON.parse(mr); } catch (e) { mr = {}; } }
  const commitStatus = Number(params.commitStatus);
  const mrStatus = Number(params.mrStatus);
  if (commitStatus !== 201) {
    return { result: 'failed', error: 'GitLab rejected the commit (HTTP ' + commitStatus + ').', mrUrl: '' };
  }
  const url = mr && typeof mr.web_url === 'string' ? mr.web_url : '';
  if (mrStatus !== 201 || !/^https:\/\//.test(url)) {
    return { result: 'failed', error: 'The intent was committed, but the merge request was not opened (HTTP ' + mrStatus + ').', mrUrl: '' };
  }
  return { result: 'ok', error: '', mrUrl: url };
};
