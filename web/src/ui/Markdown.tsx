import { memo } from "react";
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { imageIdLinkedBy, type ImageRef } from "../protocol/models";
import { ReplyImage } from "./images";

/** Keeps rad's image links, which react-markdown would drop as an unknown
 *  scheme; other URLs get its usual checks. */
function urlTransform(url: string): string {
  return url.startsWith("rad-image:") ? url : defaultUrlTransform(url);
}

const plugins = [remarkGfm];

/** An agent's Markdown (GitHub-flavoured), in the message and code fonts.
 *  Raw HTML in it shows as text: react-markdown doesn't render it. */
export const Markdown = memo(function Markdown({ text, images = [] }: { text: string; images?: ImageRef[] }) {
  const components: Components = {
    img: ({ src, alt }) => {
      const id = typeof src === "string" ? imageIdLinkedBy(src) : undefined;
      if (id) return <ReplyImage id={id} alt={alt} images={images} />;
      return <img src={typeof src === "string" ? src : undefined} alt={alt ?? ""} className="md-image" />;
    },
    a: ({ href, children }) => (
      <a href={href} target="_blank" rel="noreferrer noopener">
        {children}
      </a>
    ),
    input: ({ checked }) => <span className={"task-marker" + (checked ? " done" : "")} aria-checked={checked} role="checkbox" />,
  };
  return (
    <div className="markdown">
      <ReactMarkdown remarkPlugins={plugins} urlTransform={urlTransform} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
});
