import { useEffect, useRef, useState, type ReactNode } from "react";
import { basicSetup } from "codemirror";
import { indentWithTab } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { EditorView, keymap } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { EyeIcon, EyeSlashIcon, PencilSimpleIcon } from "@phosphor-icons/react";
import { ClusterService } from "@bindings/internal/bindings";
import type { Cluster } from "@bindings/internal/service";
import { DeleteAction } from "@/components/actions";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { CopyButton } from "@/components/copy-button";
import { objectKind, type Kind, type Target } from "@/components/targets";
import { Button } from "@/components/ui/button";
import { Inspector, InspectorActions, InspectorHeader, InspectorName, InspectorTitle } from "@/components/inspector";
import { TargetVerbs } from "@/components/target-verbs";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { yamlQuery, errorText, isConflict } from "@/queries";

type YamlProps = { cluster: Cluster; kind: Kind; namespace: string; name: string };

// lastTab is the tab the user last chose; a detail is remade for each object, and walking them keeps to it.
let lastTab = "details";

// DetailTabs puts a detail's own content and its YAML behind two tabs; the YAML is fetched when its tab opens.
export function DetailTabs({ children, ...props }: YamlProps & { children: ReactNode }) {
  return (
    <Tabs defaultValue={lastTab} onValueChange={(v) => (lastTab = v)} className="min-h-0 flex-1 gap-0">
      <TabsList variant="line" className="h-8 w-full justify-start gap-4 border-b">
        <TabsTrigger value="details" className="flex-none px-0">
          Details
        </TabsTrigger>
        <TabsTrigger value="yaml" className="flex-none px-0">
          YAML
        </TabsTrigger>
      </TabsList>
      <TabsContent value="details" className="flex min-h-0 flex-col gap-4 overflow-auto pt-3">
        {children}
      </TabsContent>
      <TabsContent value="yaml" className="flex min-h-0 flex-col pt-3">
        <YamlView {...props} />
      </TabsContent>
    </Tabs>
  );
}

export function YamlView(props: YamlProps) {
  const { cluster, kind, namespace, name } = props;
  const secret = kind === "secret";
  // The whole document is revealed at once: a YAML with one value shown and the rest masked is not the object.
  const [reveal, setReveal] = useState(false);
  const [editing, setEditing] = useState(false);
  const q = useQuery(yamlQuery(cluster.id, objectKind[kind], namespace, name, reveal));
  if (editing) return <YamlEditor {...props} onDone={() => setEditing(false)} />;
  return (
    <div className="min-h-0 flex-1 overflow-auto rounded-md bg-muted/50 px-3 py-2">
      {q.data && (
        <div className="sticky top-0 float-right flex gap-0.5 rounded-md bg-muted">
          {secret && (
            <Button variant="ghost" size="icon-xs" title={reveal ? "Hide values" : "Reveal values"} onClick={() => setReveal(!reveal)}>
              {reveal ? <EyeSlashIcon /> : <EyeIcon />}
            </Button>
          )}
          <CopyButton text={q.data} title="Copy YAML" className="opacity-100" />
          {/* The editor shows a Secret's values, so they are revealed first. */}
          {(!secret || reveal) && (
            <Button variant="ghost" size="icon-xs" title="Edit" onClick={() => setEditing(true)}>
              <PencilSimpleIcon />
            </Button>
          )}
        </div>
      )}
      {q.error ? (
        <p className="text-xs text-destructive">{errorText(q.error)}</p>
      ) : q.data ? (
        <pre className="font-mono text-xs leading-5">{highlight(q.data)}</pre>
      ) : (
        <p className="text-xs text-muted-foreground">Loading…</p>
      )}
    </div>
  );
}

// YamlEditor edits the object as text. Nothing is applied before the Cluster's dry run of the change is shown as a diff
// and confirmed; a change made in the Cluster meanwhile is a conflict, never overwritten.
function YamlEditor({ cluster, kind, namespace, name, onDone }: YamlProps & { onDone: () => void }) {
  const queryClient = useQueryClient();
  const k = objectKind[kind];
  const [original, setOriginal] = useState("");
  const [draft, setDraft] = useState<string | null>(null);
  const [diff, setDiff] = useState<string | null>(null);
  const load = useMutation({
    mutationFn: () => ClusterService.EditYAML(cluster.id, k, namespace, name),
    onSuccess: (text) => {
      setOriginal(text);
      setDraft(text);
      setDiff(null);
      review.reset();
      apply.reset();
    },
  });
  const review = useMutation({
    mutationFn: () => ClusterService.DiffYAML(cluster.id, k, namespace, name, original, draft ?? ""),
    onMutate: () => apply.reset(),
    onSuccess: setDiff,
  });
  const apply = useMutation({
    mutationFn: () => ClusterService.ApplyYAML(cluster.id, k, namespace, name, original, draft ?? ""),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["cluster", cluster.id] });
      onDone();
    },
    // A conflict leaves the dialog for the editor, where Reload is.
    onError: (e) => isConflict(e) && setDiff(null),
  });
  useEffect(() => load.mutate(), []);
  const failure = load.error ?? review.error ?? (isConflict(apply.error) ? apply.error : null);
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      {draft === null ? (
        <p className={cn("rounded-md bg-muted/50 px-3 py-2 text-xs", load.error ? "text-destructive" : "text-muted-foreground")}>
          {load.error ? errorText(load.error) : "Loading…"}
        </p>
      ) : (
        // Reload replaces the document, so the editor starts over from the text it gets.
        <CodeEditor
          key={original}
          label={`YAML of ${name}`}
          value={draft}
          onChange={(text) => {
            setDraft(text);
            setDiff(null);
          }}
        />
      )}
      {failure && draft !== null && (
        <div className="flex items-start gap-2 text-xs text-destructive">
          <p className="flex-1 whitespace-pre-wrap">
            {isConflict(failure) ? `${name} changed in the Cluster since you opened it. Reload to edit the latest version; your edits are dropped.` : errorText(failure)}
          </p>
          {isConflict(failure) && (
            <Button variant="outline" size="xs" disabled={load.isPending} onClick={() => load.mutate()}>
              Reload
            </Button>
          )}
        </div>
      )}
      {diff === "" && <p className="text-xs text-muted-foreground">Nothing would change.</p>}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onDone}>
          Cancel
        </Button>
        <Button size="sm" disabled={draft === null || review.isPending} onClick={() => review.mutate()}>
          Review changes
        </Button>
      </div>
      <ConfirmDialog
        open={!!diff}
        onOpenChange={(open) => !open && setDiff(null)}
        title={`Apply changes to ${name}?`}
        description="The Cluster checked the change in a dry run. These lines would change:"
        confirm="Apply"
        action={apply}
        className="sm:max-w-2xl"
      >
        <p className="border-l-2 border-foreground/40 pl-3 text-sm text-muted-foreground">
          on <span className="text-base font-semibold text-foreground">{cluster.name}</span>
        </p>
        <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted/50 py-2 font-mono text-xs leading-5">
          <div className="w-fit min-w-full">{diffLines(diff ?? "")}</div>
        </pre>
      </ConfirmDialog>
    </div>
  );
}

// The editor wears the read-only view's colours: keys in the primary colour, punctuation muted, values plain.
const yamlColors = HighlightStyle.define([
  { tag: tags.definition(tags.propertyName), color: "var(--primary)" },
  { tag: [tags.separator, tags.punctuation, tags.squareBracket, tags.brace, tags.meta, tags.lineComment, tags.special(tags.string)], color: "var(--muted-foreground)" },
]);

const editorTheme = EditorView.theme({
  "&": { height: "100%", fontSize: "12px", backgroundColor: "transparent" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "20px" },
  ".cm-content": { caretColor: "var(--foreground)" },
  ".cm-cursor": { borderLeftColor: "var(--foreground)" },
  ".cm-gutters": { backgroundColor: "transparent", border: "none", color: "var(--muted-foreground)" },
  ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "color-mix(in oklch, var(--foreground) 5%, transparent)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground": {
    backgroundColor: "color-mix(in oklch, var(--primary) 30%, transparent)",
  },
  ".cm-foldPlaceholder": { backgroundColor: "var(--muted)", border: "none", color: "var(--muted-foreground)" },
  ".cm-panels": { backgroundColor: "var(--popover)", color: "var(--popover-foreground)" },
  ".cm-panels.cm-panels-bottom": { borderTop: "1px solid var(--border)" },
  ".cm-textfield": { border: "1px solid var(--border)", borderRadius: "4px", backgroundColor: "transparent" },
  ".cm-button": { backgroundImage: "none", backgroundColor: "var(--muted)", border: "1px solid var(--border)", borderRadius: "4px" },
});

// CodeEditor reads value once; edits flow out through onChange. Tab indents; Escape then Tab leaves the editor.
function CodeEditor({ label, value, onChange }: { label: string; value: string; onChange: (text: string) => void }) {
  const parent = useRef<HTMLDivElement>(null);
  const changed = useRef(onChange);
  changed.current = onChange;
  useEffect(() => {
    const view = new EditorView({
      doc: value,
      parent: parent.current!,
      extensions: [
        basicSetup,
        keymap.of([indentWithTab]),
        yaml(),
        syntaxHighlighting(yamlColors),
        editorTheme,
        EditorView.contentAttributes.of({ "aria-label": label }),
        EditorView.updateListener.of((u) => u.docChanged && changed.current(u.state.doc.toString())),
      ],
    });
    view.focus();
    return () => view.destroy();
  }, []);
  // isolate keeps CodeMirror's own z-indexes (its search panel is at 300) under the confirm dialog.
  return <div ref={parent} className="isolate min-h-0 flex-1 overflow-hidden rounded-md bg-muted/50 py-1" />;
}

// diffLines colours a unified diff; a hunk header becomes a break between the changed places.
function diffLines(diff: string) {
  return diff.trimEnd().split("\n").map((line, i) =>
    line.startsWith("@@") ? (
      i > 0 && <div key={i} className="my-1 border-t border-dashed" />
    ) : (
      <div
        key={i}
        className={cn(
          "px-3",
          line[0] === "+" && "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
          line[0] === "-" && "bg-destructive/10 text-destructive",
        )}
      >
        {line}
      </div>
    ),
  );
}

const keyLine = /^(\s*(?:- )?)([^\s#-][^:]*?|-)(:)(?=\s|$)(.*)$/;
const blockStart = /^[|>][-+\d]*$/;

// highlight colours keys and punctuation; the lines of a block scalar are left as one value, colons and all.
function highlight(yaml: string) {
  const out: ReactNode[] = [];
  let blockIndent = -1;
  yaml.trimEnd().split("\n").forEach((line, i) => {
    const indent = line.search(/\S|$/);
    if (blockIndent >= 0 && (indent > blockIndent || line.trim() === "")) {
      out.push(line, "\n");
      return;
    }
    blockIndent = -1;
    const m = keyLine.exec(line);
    if (!m) {
      const item = /^(\s*- )(.*)$/.exec(line);
      out.push(item ? <span key={i}><span className="text-muted-foreground">{item[1]}</span>{item[2]}</span> : line, "\n");
      return;
    }
    const [, lead, key, colon, rest] = m;
    const value = rest.trim();
    if (blockStart.test(value)) blockIndent = lead.length;
    out.push(
      <span key={i}>
        <span className="text-muted-foreground">{lead}</span>
        <span className="text-primary">{key}</span>
        <span className="text-muted-foreground">{colon}</span>
        {blockIndent >= 0 ? <span className="text-muted-foreground">{rest}</span> : rest}
      </span>,
      "\n",
    );
  });
  return out;
}

// YamlDetail is the detail of an object that has nothing to explain beyond its manifest.
export function YamlDetail({ cluster, target, onForward, onClose }: { cluster: Cluster; target: Target; onForward?: () => void; onClose: () => void }) {
  return (
    <Inspector onClose={onClose}>
      <InspectorHeader>
        <InspectorTitle>
          <InspectorName kind={target.kind} namespace={target.namespace} name={target.name} />
          <DeleteAction cluster={cluster} target={target} onDone={onClose} />
        </InspectorTitle>
        <InspectorActions open={onForward && <TargetVerbs cluster={cluster} target={target} onForward={onForward} onLeave={onClose} />} />
      </InspectorHeader>
      <YamlView cluster={cluster} kind={target.kind} namespace={target.namespace} name={target.name} />
    </Inspector>
  );
}
