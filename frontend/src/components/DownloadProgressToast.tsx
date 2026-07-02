import { useDownloadProgress } from "@/hooks/useDownloadProgress";
import { useDownloadQueueData } from "@/hooks/useDownloadQueueData";
import { Download, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
interface DownloadProgressToastProps {
    onClick: () => void;
    pendingBatchCount?: number;
}
export function DownloadProgressToast({ onClick, pendingBatchCount = 0 }: DownloadProgressToastProps) {
    const progress = useDownloadProgress();
    const queueInfo = useDownloadQueueData();
    const remaining = queueInfo.queued_count;
    const hasActiveDownloads = queueInfo.queue.some(item => item.status === "queued" || item.status === "downloading");
    if (!hasActiveDownloads) {
        return null;
    }
    // Between tracks, is_downloading/speed briefly drop to 0 and mb resets; falling
    // back to the remaining-track count avoids a "0.00 MB" flicker mid-run.
    const isTransferring = progress.speed_mbps > 0 || progress.mb_downloaded > 0;
    return (<div className="fixed bottom-4 left-[calc(56px+1rem)] z-50 animate-in slide-in-from-bottom-5 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-bottom-5">
      <Button variant="outline" className="h-auto cursor-pointer rounded-lg border-border bg-background p-3 text-foreground shadow-lg transition-colors hover:bg-muted dark:border-blue-800 dark:bg-blue-950 dark:text-blue-100 dark:hover:bg-blue-900" onClick={onClick}>
        <div className="flex items-center gap-3">
          <Download className={`h-4 w-4 text-blue-600 dark:text-blue-400 ${progress.is_downloading ? 'animate-bounce' : ''}`}/>
          <div className="flex flex-col min-w-[80px]">
            {isTransferring ? (<>
              <p className="text-sm font-medium font-mono tabular-nums">
                {progress.mb_downloaded.toFixed(2)} MB
              </p>
              {progress.speed_mbps > 0 && (<p className="text-xs font-mono tabular-nums text-muted-foreground dark:text-blue-300">
                  {progress.speed_mbps.toFixed(2)} MB/s
                </p>)}
            </>) : (<p className="text-sm font-medium">
              {progress.status_text || (remaining > 0 ? `${remaining} track${remaining === 1 ? "" : "s"} left` : "Preparing…")}
            </p>)}
            {isTransferring && remaining > 0 && (<p className="text-xs text-muted-foreground dark:text-blue-300">
              {remaining} track{remaining === 1 ? "" : "s"} left
            </p>)}
            {pendingBatchCount > 0 && (<p className="text-xs text-muted-foreground dark:text-blue-300">
              +{pendingBatchCount} playlist{pendingBatchCount === 1 ? "" : "s"} queued
            </p>)}
          </div>
          <ChevronRight className="ml-1 h-4 w-4 text-muted-foreground dark:text-blue-300"/>
        </div>
      </Button>
    </div>);
}
