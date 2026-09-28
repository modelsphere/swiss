import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState, Loading } from "@/components/States";
import { ProfileEditor } from "@/components/ProfileEditor";
import { useSessionQuery } from "@/components/Session";

// Setup is the first run: swissd owns the site profile, the chart writes none,
// so a fresh install has nothing to compose against until this is filled in.
//
// Pre-filled from the server's own default rather than from a blank page: the
// answer to "what goes in here" is most of the work, and the default is a
// working profile with the reasoning beside each field.
export function Setup() {
  const navigate = useNavigate();
  const session = useSessionQuery();
  const template = useQuery({ queryKey: ["profileTemplate"], queryFn: api.profileTemplate });
  const existing = useQuery({ queryKey: ["profile"], queryFn: api.profile, retry: false });

  // Not logged in is the gate's business; this page is always rendered inside
  // it. Navigating from a render body here would be a second answer to one
  // question.
  if (template.isPending) return <Loading what="the setup" />;
  if (template.error) return <ErrorState what="the setup" error={template.error} />;

  const configured = !!session.data?.initialized;
  // An install that got half-way through keeps what it saved; everything else
  // starts from the server's own default.
  //
  // The two halves are chosen separately on purpose. A stored document that
  // does not parse has no profile to seed the form with -- that falls back to
  // the default -- but its text is exactly what has to be edited, so the YAML
  // tab opens on it rather than on a template that would discard the whole
  // profile to fix one line.
  const startProfile = existing.data?.profile ?? template.data.profile;
  const startYaml = existing.data?.yaml ?? template.data.yaml ?? "";

  // The server's own default failing to parse is a bug in swissd, not something
  // this page can seed around.
  if (!startProfile) {
    return <ErrorState what="the setup" error={new Error("the default profile does not parse")} />;
  }

  return (
    <div className="mx-auto max-w-3xl space-y-5 px-4 py-8">
      <div>
        <h1 className="text-xl font-semibold">Set up this cluster</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          The site profile is the cluster-shaped layer: what the public catalog cannot know
          and a deploy form should not have to retype — where charts come from, where weights
          sit on disk, which entrypoint publishes a route. swissd writes it to a ConfigMap in
          this cluster and composes every deploy against it.
        </p>
      </div>

      {existing.data?.error && (
        <Card>
          <CardContent className="p-4 text-sm">
            <div className="font-medium text-warning">
              The stored profile does not parse, so swissd has nothing to compose against.
            </div>
            <pre className="mt-2 overflow-x-auto font-mono text-xs text-muted-foreground">
              {existing.data.error}
            </pre>
            <p className="mt-2 text-muted-foreground">
              The YAML tab below opens on the stored document — fix the line it names and
              save. The form tab starts from the default instead, and saving from it would
              replace the whole profile.
            </p>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Site profile</CardTitle>
          <p className="text-sm text-muted-foreground">
            Pre-filled with a working default. Everything here can be changed later on the
            Site profile page.
          </p>
        </CardHeader>
        <CardContent>
          <ProfileEditor
            profile={startProfile}
            yaml={startYaml}
            submitLabel={configured ? "Save profile" : "Save and start"}
            onSaved={() => navigate("/", { replace: true })}
          />
        </CardContent>
      </Card>
    </div>
  );
}
