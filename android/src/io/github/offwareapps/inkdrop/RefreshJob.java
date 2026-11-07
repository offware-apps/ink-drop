package io.github.offwareapps.inkdrop;

import android.app.job.JobInfo;
import android.app.job.JobParameters;
import android.app.job.JobScheduler;
import android.app.job.JobService;
import android.content.ComponentName;
import android.content.Context;
import java.util.List;

// RefreshJob refreshes the feeds on its own. Android starts it once per chosen
// interval and only while the tablet is online, so a refresh due while offline
// waits for the next connection.
public class RefreshJob extends JobService {
  private static final int ID = 1;
  // The intervals the Feeds tab offers, in hours; 0 turns the refresh off.
  static final int[] HOURS = {0, 1, 3, 6, 12};

  // ensure schedules the refresh again where it is due but gone, as after a
  // force-stop or the Boox freezing the app, which both cancel it.
  static void ensure(Context ctx, int hours) {
    if (hours > 0 && ctx.getSystemService(JobScheduler.class).getPendingJob(ID) == null) schedule(ctx, hours);
  }

  static void schedule(Context ctx, int hours) {
    JobScheduler js = ctx.getSystemService(JobScheduler.class);
    if (hours == 0) {
      js.cancel(ID);
      return;
    }
    js.schedule(new JobInfo.Builder(ID, new ComponentName(ctx, RefreshJob.class))
        .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
        .setPeriodic(hours * 3_600_000L)
        // Kept across a restart of the tablet.
        .setPersisted(true)
        .build());
  }

  // One run of the job: its refresher, and whether Android stopped it. A run
  // still waiting on an earlier one's lock keeps its own, so a stop always
  // reaches the run it was sent to.
  private static final class Run {
    final Refresher refresher;
    volatile boolean stopped;

    Run(Refresher r) {
      refresher = r;
    }
  }

  private volatile Run current;

  @Override
  public boolean onStartJob(JobParameters params) {
    Run run = new Run(new Refresher(this));
    current = run;
    new Thread(() -> {
      List<Feeds.Feed> list = run.refresher.feeds();
      if (!list.isEmpty() && run.refresher.folder() != null) run.refresher.refresh(list, "on its own");
      // A job Android stopped is finished already; it reschedules it itself.
      if (!run.stopped) jobFinished(params, false);
    }).start();
    return true;
  }

  @Override
  public boolean onStopJob(JobParameters params) {
    // Android stopped the job, the network gone for one: the stage running is
    // killed, what it left waits for the next run, and the job runs again later.
    Run run = current;
    if (run != null) {
      run.stopped = true;
      run.refresher.cancel();
    }
    return true;
  }
}
