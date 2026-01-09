import { HardDrive, FileVideo } from 'lucide-react'
import { Stats } from '@/types'

interface HeaderProps {
  stats: Stats | null
}

export function Header({ stats }: HeaderProps) {
  return (
    <header className="border-b bg-card">
      <div className="container mx-auto px-4 py-6">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-3xl font-bold flex items-center gap-3">
              <img src="/icon.png" alt="TeleVideo Converter" className="h-10 w-10 rounded-lg" />
              TeleVideo Converter
            </h1>
          </div>

          {stats && (
            <div className="flex gap-6 text-sm">
              <div className="flex items-center gap-2">
                <HardDrive className="h-4 w-4 text-muted-foreground" />
                <span className="text-muted-foreground">
                  {stats.used_space_gb} / {stats.max_space_gb} GB used
                </span>
              </div>
              <div className="flex items-center gap-2">
                <FileVideo className="h-4 w-4 text-muted-foreground" />
                <span className="text-muted-foreground">
                  {stats.total_videos} files
                </span>
              </div>
            </div>
          )}
        </div>
      </div>
    </header>
  )
}
