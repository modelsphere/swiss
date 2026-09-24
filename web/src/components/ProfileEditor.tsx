import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type SiteProfile } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/States";
import { ProfileForm } from "@/components/ProfileForm";

type Mode = "form" | "yaml";

// ProfileEditor is the one place the site profile is written, used by the
// first-run setup and by the profile page.
//
// The form is the editor. The profile is a fixed set of cluster facts, each
// with a name and a reason, and a text box makes an operator responsible for
// YAML indentation on top of the actual question. The text mode stays for the
// two things a form cannot hold: comments, and `extra` -- free-form site values
// no named field covers.
export function ProfileEditor({
  profile,
  yaml: storedYaml,
  submitLabel,
  onSaved,
}: {
  profile: SiteProfile;
  yaml: string;
  submitLabel: string;
  onSaved?: () => void;
}) {
  const [mode, setMode] = useState<Mode>("form");
  const [form, setForm] = useState<SiteProfile>(profile);
  const [yaml, setYaml] = useState(storedYaml);
  const qc = useQueryClient();

  const save = useMutation({
    // Two shapes, one endpoint: the form sends the object and swissd renders
    // it with the same library that reads it; the text mode sends the document
    // as typed, so comments survive.
    mutationFn: () => (mode === "form" ? api.saveProfile({ profile: form }) : api.saveProfile({ yaml })),
    onSuccess: (saved) => {
      qc.setQueryData(["profile"], saved);
      qc.invalidateQueries({ queryKey: ["session"] });
      qc.invalidateQueries({ queryKey: ["cluster"] });
      // Whichever half was not edited is now stale. A save that came back
      // without a profile did not parse, which the server refuses -- so this is
      // only ever the parsed result of what was just written.
      if (saved.profile) setForm(saved.profile);
      setYaml(saved.yaml ?? "");
      onSaved?.();
    },
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-1">
        <Tab active={mode === "form"} onClick={() => setMode("form")}>
          Form
        </Tab>
        <Tab active={mode === "yaml"} onClick={() => setMode("yaml")}>
          YAML
        </Tab>
      </div>

      {mode === "form" ? (
        <>
          <ProfileForm value={form} onChange={setForm} />
          {(storedYaml.includes("#") || !!profile.extra) && (
            <p className="text-xs text-muted-foreground">
              Saving from the form rewrites the document, so comments in it are lost.
              {profile.extra && " Values under extra: are carried through untouched; edit them in YAML."}
            </p>
          )}
        </>
      ) : (
        <textarea
          value={yaml}
          onChange={(e) => setYaml(e.target.value)}
          spellCheck={false}
          rows={24}
          className="w-full rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed"
        />
      )}

      {/* The server parses before it stores: a profile that does not parse
          would take the deploy form down until somebody edited a ConfigMap by
          hand, so the refusal arrives here instead. */}
      {save.error && <ErrorState what="the profile" error={save.error} />}

      <div className="flex items-center gap-3">
        <Button
          onClick={() => save.mutate()}
          disabled={save.isPending || (mode === "yaml" ? !yaml.trim() : !form.name.trim())}
        >
          {save.isPending ? "Saving…" : submitLabel}
        </Button>
        {save.isSuccess && !save.isPending && (
          <span className="text-sm text-success">Saved. swissd composes against it now.</span>
        )}
      </div>
    </div>
  );
}

function Tab({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        active
          ? "rounded-md bg-muted px-3 py-1.5 text-sm font-medium"
          : "rounded-md px-3 py-1.5 text-sm text-muted-foreground hover:bg-muted/60"
      }
    >
      {children}
    </button>
  );
}
