import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";

/** Renders user/editor Markdown. Raw HTML is not parsed and everything goes through rehype-sanitize. */
export function Markdown({ children, lang }: { children: string; lang?: string }) {
  return (
    <div lang={lang} className="markdown-body">
      <ReactMarkdown
        rehypePlugins={[rehypeSanitize]}
        components={{
          a: ({ href, children: c }) => {
            const safe = typeof href === "string" && /^(https?:|mailto:|\/|#)/i.test(href);
            const external = typeof href === "string" && /^https?:/i.test(href);
            return safe ? <a href={href} rel={external ? "noopener noreferrer nofollow ugc" : undefined}>{c}</a> : <span>{c}</span>;
          },
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
