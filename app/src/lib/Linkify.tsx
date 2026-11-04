import { openUrl } from "@tauri-apps/plugin-opener";

const URL_RE = /(https?:\/\/[^\s<>"')\]]+[^\s<>"')\].,;:!?])/g;

export function Linkify({ text }: { text: string }) {
  return (
    <>
      {text.split(URL_RE).map((part, i) =>
        i % 2 === 1 ? (
          <a
            key={i}
            href={part}
            className="inline-link"
            onClick={(e) => {
              e.preventDefault();
              void openUrl(part);
            }}
          >
            {part}
          </a>
        ) : (
          part
        ),
      )}
    </>
  );
}
