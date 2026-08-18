import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FlaskConical, Image as ImageIcon, MessageSquareText, Video } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { ErrorState, LoadingState } from "@/shared/components/data-state";
import { runtimeConfig } from "@/shared/config/runtime-config";
import { ApiError } from "@/shared/api/client";
import { cn } from "@/shared/lib/cn";
import {
  listAccountModels,
  testAccountModel,
  type AccountDTO,
  type AccountModelTestResultDTO,
  type AccountTestCapability,
  type AccountTestModelDTO,
} from "@/features/accounts/accounts-api";

const defaultMediaPrompt = "小猫在天上";

type Props = {
  account: AccountDTO;
  onClose: () => void;
};

export function AccountModelTestDialog({ account, onClose }: Props) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [prompt, setPrompt] = useState(defaultMediaPrompt);
  const [results, setResults] = useState<Record<string, AccountModelTestResultDTO>>({});
  const [pendingKey, setPendingKey] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const modelsQuery = useQuery({
    queryKey: ["account-test-models", account.id],
    queryFn: () => listAccountModels(account.id),
  });

  useEffect(() => () => {
    abortRef.current?.abort();
  }, []);

  const grouped = useMemo(() => groupAccountTestModels(modelsQuery.data?.items ?? []), [modelsQuery.data?.items]);

  const testMutation = useMutation({
    mutationFn: async (model: AccountTestModelDTO) => {
      abortRef.current?.abort();
      const controller = new AbortController();
      abortRef.current = controller;
      setPendingKey(modelKey(model));
      return testAccountModel(account.id, {
        publicId: model.publicId,
        capability: model.capability,
        prompt: isMediaCapability(model.capability) ? prompt.trim() || defaultMediaPrompt : undefined,
      }, controller.signal);
    },
    onSuccess: (result, model) => {
      setResults((current) => ({ ...current, [modelKey(model)]: result }));
      if (model.capability === "video" || (result.outcome === "flagged" && model.capability !== "image")) {
        void queryClient.invalidateQueries({ queryKey: ["accounts"] });
        void queryClient.invalidateQueries({ queryKey: ["accounts", "summary"] });
      }
    },
    onError: (error, model) => {
      if (isAbortError(error)) return;
      setResults((current) => ({
        ...current,
        [modelKey(model)]: {
          outcome: "error",
          publicId: model.publicId,
          capability: model.capability,
          error: error instanceof ApiError ? error.message : t("apiErrors.requestFailed"),
        },
      }));
    },
    onSettled: () => {
      setPendingKey(null);
    },
  });

  function closeDialog(): void {
    abortRef.current?.abort();
    onClose();
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) closeDialog(); }}>
      <DialogContent className="flex max-h-[calc(100dvh-2rem)] max-w-2xl flex-col gap-4 overflow-hidden sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <FlaskConical className="size-4" />
            {t("accounts.modelTest.title")}
          </DialogTitle>
          <DialogDescription>{t("accounts.modelTest.description", { name: account.name })}</DialogDescription>
        </DialogHeader>

        <div className="space-y-1.5">
          <Label htmlFor="account-model-test-prompt">{t("accounts.modelTest.mediaPrompt")}</Label>
          <Input
            id="account-model-test-prompt"
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            placeholder={defaultMediaPrompt}
          />
          <p className="text-xs text-muted-foreground">{t("accounts.modelTest.mediaPromptHint")}</p>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
          {modelsQuery.isPending ? <LoadingState /> : null}
          {modelsQuery.isError ? <ErrorState message={t("accounts.modelTest.loadFailed")} onRetry={() => void modelsQuery.refetch()} /> : null}
          {modelsQuery.data && grouped.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">{t("accounts.modelTest.empty")}</p>
          ) : null}
          <div className="space-y-5">
            {grouped.map((group) => (
              <section key={group.kind} className="space-y-2">
                <h3 className="flex items-center gap-2 text-sm font-medium">
                  <group.icon className="size-3.5 text-muted-foreground" />
                  {t(group.labelKey)}
                </h3>
                <div className="divide-y rounded-md border">
                  {group.items.map((model) => {
                    const key = modelKey(model);
                    const pending = pendingKey === key && testMutation.isPending;
                    const result = results[key];
                    return (
                      <div key={key} className="space-y-2 px-3 py-2.5">
                        <div className="flex items-center gap-3">
                          <div className="min-w-0 flex-1">
                            <p className="truncate text-sm font-medium">{model.publicId}</p>
                            <p className="truncate text-xs text-muted-foreground">{model.upstreamModel}</p>
                          </div>
                          <Button size="sm" variant="outline" disabled={testMutation.isPending} onClick={() => testMutation.mutate(model)}>
                            {pending ? <Spinner /> : t("accounts.modelTest.run")}
                          </Button>
                        </div>
                        {result ? <ModelTestResult result={result} /> : null}
                      </div>
                    );
                  })}
                </div>
              </section>
            ))}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ModelTestResult({ result }: { result: AccountModelTestResultDTO }) {
  const { t } = useTranslation();
  const previewUrl = result.previewUrl ? resolvePreviewURL(result.previewUrl) : "";
  const outcome = result.capability === "image" && result.outcome === "flagged" ? "error" : result.outcome;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={outcome === "ok" ? "default" : outcome === "flagged" ? "destructive" : "secondary"}>
          {t(`accounts.modelTest.outcome.${outcome}`)}
        </Badge>
        {result.error ? <p className="text-xs text-destructive">{result.error}</p> : null}
      </div>
      {result.text ? (
        <pre className="max-h-40 overflow-auto rounded-md bg-muted/50 p-2 text-xs whitespace-pre-wrap break-all">{result.text}</pre>
      ) : null}
      {previewUrl && result.capability === "image" ? (
        <img src={previewUrl} alt={t("accounts.modelTest.imagePreview")} className={cn("max-h-64 rounded-md border object-contain")} />
      ) : null}
      {previewUrl && result.capability === "video" ? (
        <video
          src={previewUrl}
          controls
          playsInline
          preload="metadata"
          className="max-h-64 w-full rounded-md border bg-black"
          onLoadedMetadata={(event) => showFirstVideoFrame(event.currentTarget)}
        />
      ) : null}
    </div>
  );
}

function modelKey(model: AccountTestModelDTO): string {
  return `${model.capability}:${model.publicId}`;
}

function isMediaCapability(capability: AccountTestCapability): boolean {
  return capability === "image" || capability === "video";
}

function groupAccountTestModels(items: AccountTestModelDTO[]): Array<{
  kind: "text" | "image" | "video";
  labelKey: string;
  icon: typeof MessageSquareText;
  items: AccountTestModelDTO[];
}> {
  const text = items.filter((item) => item.capability === "chat" || item.capability === "responses");
  const image = items.filter((item) => item.capability === "image");
  const video = items.filter((item) => item.capability === "video");
  return [
    { kind: "text" as const, labelKey: "accounts.modelTest.groupText", icon: MessageSquareText, items: text },
    { kind: "image" as const, labelKey: "accounts.modelTest.groupImage", icon: ImageIcon, items: image },
    { kind: "video" as const, labelKey: "accounts.modelTest.groupVideo", icon: Video, items: video },
  ].filter((group) => group.items.length > 0);
}

function isAbortError(error: unknown): boolean {
  return (error instanceof DOMException || error instanceof Error) && error.name === "AbortError";
}

function resolvePreviewURL(value: string): string {
  const url = value.trim();
  if (!url || url.startsWith("data:") || url.startsWith("blob:")) return url;
  try {
    const base = runtimeConfig.publicApiBaseUrl || window.location.origin;
    const resolved = new URL(url, `${base}/`);
    if (resolved.pathname.startsWith("/v1/media/")) {
      return `${resolved.pathname}${resolved.search}${resolved.hash}`;
    }
    return resolved.toString();
  } catch {
    return url;
  }
}

function showFirstVideoFrame(video: HTMLVideoElement): void {
  if (!Number.isFinite(video.duration) || video.duration <= 0) return;
  video.currentTime = Math.min(0.01, video.duration / 2);
}
