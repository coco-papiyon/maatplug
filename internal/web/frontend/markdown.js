// Bundled markdown-it renders Markdown with embedded HTML disabled. Its default
// URL validator rejects executable schemes, including encoded javascript URLs.
const messageMarkdown = window.markdownit({
  html: false,
  breaks: true,
  linkify: false,
  typographer: false,
});

const defaultMessageLink = messageMarkdown.renderer.rules.link_open;
messageMarkdown.renderer.rules.link_open = (tokens, index, options, env, renderer) => {
  tokens[index].attrSet("target", "_blank");
  tokens[index].attrSet("rel", "noopener noreferrer");
  return defaultMessageLink
    ? defaultMessageLink(tokens, index, options, env, renderer)
    : renderer.renderToken(tokens, index, options);
};

function renderMarkdown(text) {
  return messageMarkdown.render(String(text ?? ""));
}
