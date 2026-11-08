package io.github.offwareapps.inkdrop;

import android.app.Activity;
import android.content.Intent;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.graphics.Color;
import android.graphics.Paint;
import android.graphics.Typeface;
import android.graphics.drawable.ColorDrawable;
import android.graphics.drawable.Drawable;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.LayerDrawable;
import android.net.Uri;
import android.os.Bundle;
import android.provider.DocumentsContract;
import android.provider.DocumentsContract.Document;
import android.text.Editable;
import android.text.TextUtils;
import android.text.TextWatcher;
import android.text.format.DateUtils;
import android.view.Gravity;
import android.view.View;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.view.inputmethod.EditorInfo;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.app.AlertDialog;
import android.view.GestureDetector;
import android.view.KeyEvent;
import android.view.MotionEvent;
import java.util.function.Supplier;
import android.widget.TextView;
import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

// One activity, six screens: the books, the feeds, the logs, the settings,
// one feed's settings, and adding a feed. It is
// drawn for a greyscale e-ink screen: black, white and two greys, flat shapes,
// large type, and nothing that animates.
public class MainActivity extends Activity {
  private static final int PICK_FOLDER = 1, IMPORT = 2, EXPORT = 3;
  private static final Pattern URL = Pattern.compile("https?://\\S+", Pattern.CASE_INSENSITIVE);
  private static final int INK = Color.BLACK, PAPER = Color.WHITE;
  private static final int GREY = 0xFF555555, PALE = 0xFFE6E6E6;

  private enum Screen { BOOKS, FEEDS, LOGS, SETTINGS, FEED, ADD }

  private SharedPreferences prefs;
  private Feeds feeds;
  private List<Feeds.Feed> directory;
  private Screen screen = Screen.BOOKS;
  // The screen is four parts: body, the fixed top; list, one page of rows;
  // pager, the bar that turns pages; foot, the line under it.
  private LinearLayout body, list, pager, foot;
  // rows makes each row of the shown list, built only when its page shows, at
  // rowDp tall; page is the page shown.
  private final List<Supplier<View>> rows = new ArrayList<>();
  private int rowDp = 100;
  private int page;
  private int listHeight;
  private GestureDetector swipe;
  private String status = "";
  // The folder the Books tab shows, or null for every book, and the section
  // inside it, or null for the whole folder.
  private String filter;
  private String section;
  // today keeps the Books tab to the last 24 hours.
  private boolean today;
  // editing is the address of the feed whose settings show.
  private String editing;
  // A book arriving after newSince, when the user last left the app, is new
  // until opened. away is set while a book or a picker the app opened shows.
  private long newSince = Long.MAX_VALUE;
  private boolean away;
  // leaving keeps the words typed on the screen shown, run before it goes.
  private final List<Runnable> leaving = new ArrayList<>();
  private Refresher refresher;
  private boolean busy;

  @Override
  protected void onCreate(Bundle state) {
    super.onCreate(state);
    prefs = getSharedPreferences("ink-drop", MODE_PRIVATE);
    feeds = new Feeds(new File(getFilesDir(), "feeds.opml"));
    refresher = new Refresher(this);
    // The refresh runs on its own every 3 hours until the user picks otherwise.
    if (!prefs.contains("auto-hours")) {
      prefs.edit().putInt("auto-hours", 3).apply();
      RefreshJob.schedule(this, 3);
    }
    RefreshJob.ensure(this, prefs.getInt("auto-hours", 3));
    LinearLayout root = new LinearLayout(this);
    root.setOrientation(LinearLayout.VERTICAL);
    // Android 15 draws an app under the system bars unless it asks for room.
    root.setFitsSystemWindows(true);
    root.setBackgroundColor(PAPER);
    body = column(dp(24), dp(24), dp(24), 0);
    list = column(dp(24), dp(8), dp(24), 0);
    pager = new LinearLayout(this);
    pager.setGravity(Gravity.CENTER_VERTICAL);
    pager.setPadding(dp(24), dp(4), dp(24), dp(4));
    foot = column(dp(24), 0, dp(24), dp(16));
    root.addView(body);
    root.addView(list, new LinearLayout.LayoutParams(-1, 0, 1));
    // The pager keeps its height on a one-page list, so the list never resizes
    // when the bar empties and the page count holds still.
    root.addView(pager, new LinearLayout.LayoutParams(-1, dp(56)));
    root.addView(foot);
    setContentView(root);
    list.addOnLayoutChangeListener((v, l, t, r, b, ol, ot, or, ob) -> {
      if (b - t != listHeight) {
        listHeight = b - t;
        v.post(this::fill);
      }
    });
    swipe = new GestureDetector(this, new GestureDetector.SimpleOnGestureListener() {
      @Override
      public boolean onFling(MotionEvent a, MotionEvent b, float vx, float vy) {
        if (a == null || Math.abs(b.getX() - a.getX()) < dp(80) || Math.abs(vx) < Math.abs(vy) * 2) return false;
        turn(vx < 0 ? 1 : -1);
        return true;
      }
    });
    // The page takes the whole screen; a swipe from the top shows the status bar.
    WindowInsetsController bars = getWindow().getInsetsController();
    bars.hide(WindowInsets.Type.statusBars());
    bars.setSystemBarsBehavior(WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE);
    show(Screen.BOOKS);
    // A screen rebuilt, after a rotation for one, has handled its share already.
    if (state == null) shared(getIntent());
  }

  // A horizontal swipe anywhere turns the page, as in the tablet's reader.
  @Override
  public boolean dispatchTouchEvent(MotionEvent e) {
    swipe.onTouchEvent(e);
    return super.dispatchTouchEvent(e);
  }

  @Override
  public boolean onKeyDown(int code, KeyEvent e) {
    if (code == KeyEvent.KEYCODE_PAGE_DOWN || code == KeyEvent.KEYCODE_VOLUME_DOWN) return turn(1);
    if (code == KeyEvent.KEYCODE_PAGE_UP || code == KeyEvent.KEYCODE_VOLUME_UP) return turn(-1);
    return super.onKeyDown(code, e);
  }

  @Override
  protected void onStart() {
    super.onStart();
    if (!away) newSince = prefs.getLong("looked", System.currentTimeMillis());
    away = false;
  }

  @Override
  protected void onPause() {
    super.onPause();
    for (Runnable r : new ArrayList<>(leaving)) r.run();
  }

  @Override
  protected void onStop() {
    super.onStop();
    if (!away && !isChangingConfigurations()) prefs.edit().putLong("looked", System.currentTimeMillis()).remove("opened").apply();
  }

  @Override
  protected void onResume() {
    super.onResume();
    // Another screen of the app, opened by a share, may have changed the list.
    feeds = new Feeds(new File(getFilesDir(), "feeds.opml"));
    // The list shown stays, its page with it, while the folder is read again.
    if (shelfCache == null || folder() == null) {
      shelfCache = null;
      if (screen == Screen.BOOKS) show(Screen.BOOKS);
    } else {
      load();
    }
  }

  @Override
  protected void onNewIntent(Intent intent) {
    super.onNewIntent(intent);
    setIntent(intent);
    shared(intent);
  }

  // Back goes from adding a feed or from a feed's settings to Feeds, from any
  // other tab to Books, and only from Books out of the app: the callback is
  // held while Back has somewhere to go inside it.
  private final android.window.OnBackInvokedCallback back = () -> show(screen == Screen.ADD || screen == Screen.FEED ? Screen.FEEDS : Screen.BOOKS);
  private boolean backHeld;

  private void holdBack(boolean hold) {
    if (hold == backHeld) return;
    if (hold) getOnBackInvokedDispatcher().registerOnBackInvokedCallback(android.window.OnBackInvokedDispatcher.PRIORITY_DEFAULT, back);
    else getOnBackInvokedDispatcher().unregisterOnBackInvokedCallback(back);
    backHeld = hold;
  }

  // shared saves the first address in a page shared from another app.
  private void shared(Intent intent) {
    if (!Intent.ACTION_SEND.equals(intent.getAction())) return;
    // A share reopened from Recents was handled when it first came.
    if ((intent.getFlags() & Intent.FLAG_ACTIVITY_LAUNCHED_FROM_HISTORY) != 0) return;
    CharSequence shared = intent.getCharSequenceExtra(Intent.EXTRA_TEXT);
    String text = shared == null ? null : shared.toString();
    Matcher m = URL.matcher(text == null ? "" : text);
    show(Screen.BOOKS);
    // A sentence around the address leaves its closing marks on it.
    if (m.find()) make(trimShared(m.group()));
    else setStatus("That share holds no web address.");
  }

  // trimShared drops the marks a sentence leaves after an address, and a
  // closing parenthesis only while the address holds more ")" than "(", so a
  // Wikipedia title such as Python_(programming_language) stays whole.
  static String trimShared(String url) {
    String u = url;
    while (!u.isEmpty()) {
      char c = u.charAt(u.length() - 1);
      if (".,;:!?'\"»”".indexOf(c) >= 0) u = u.substring(0, u.length() - 1);
      else if (c == ')' && u.chars().filter(x -> x == ')').count() > u.chars().filter(x -> x == '(').count()) u = u.substring(0, u.length() - 1);
      else break;
    }
    return u;
  }

  // ---- Screens ----

  private void show(Screen s) {
    // Words typed on the screen leaving are kept first.
    List<Runnable> typed = new ArrayList<>(leaving);
    leaving.clear();
    for (Runnable r : typed) r.run();
    // A feed removed meanwhile has no settings to show.
    if (s == Screen.FEED && feeds.get(editing) == null) s = Screen.FEEDS;
    if (s != screen) page = 0;
    screen = s;
    body.removeAllViews();
    foot.removeAllViews();
    rows.clear();
    hideKeyboard();
    holdBack(s != Screen.BOOKS);
    if (s == Screen.ADD) {
      addScreen();
    } else if (s == Screen.FEED) {
      feedScreen();
    } else {
      header();
      tabs();
      if (s == Screen.BOOKS) booksScreen();
      else if (s == Screen.FEEDS) feedsScreen();
      else if (s == Screen.SETTINGS) settingsScreen();
      else logsScreen();
    }
    fill();
  }

  // ---- Pages ----

  // fill shows the page of rows that fits the list's height, and the bar that
  // turns it; a page past the last, after a removal, falls back to the last.
  private void fill() {
    list.removeAllViews();
    pager.removeAllViews();
    int tall = Math.round(dp(rowDp) * Math.max(1f, getResources().getConfiguration().fontScale));
    int per = Math.max(1, list.getHeight() / tall);
    int pages = Math.max(1, (rows.size() + per - 1) / per);
    page = Math.max(0, Math.min(page, pages - 1));
    for (int i = page * per; i < Math.min(rows.size(), (page + 1) * per); i++) {
      list.addView(rows.get(i).get(), new LinearLayout.LayoutParams(-1, tall));
    }
    if (pages > 1) {
      // The first page has no Previous and the last no Next; each keeps its
      // room, so the page count holds still.
      TextView back = pill("‹  Previous", v -> turn(-1));
      if (page == 0) back.setVisibility(View.INVISIBLE);
      pager.addView(back);
      TextView where = text("Page " + (page + 1) + " of " + pages, 16, GREY);
      where.setGravity(Gravity.CENTER);
      pager.addView(where, new LinearLayout.LayoutParams(0, -2, 1));
      TextView next = pill("Next  ›", v -> turn(1));
      if (page == pages - 1) next.setVisibility(View.INVISIBLE);
      pager.addView(next);
    } else {
      pager.addView(new View(this), new LinearLayout.LayoutParams(0, 1, 1));
    }
    // The list-density switch, on the Books tab: ≡ packs the rows, ☰ spreads them.
    if (screen == Screen.BOOKS && !rows.isEmpty()) {
      boolean compact = prefs.getBoolean("compact", false);
      TextView density = text(compact ? "☰" : "≡", 26, INK);
      density.setGravity(Gravity.CENTER);
      density.setContentDescription(compact ? "Normal rows" : "Compact rows");
      density.setOnClickListener(v -> {
        prefs.edit().putBoolean("compact", !compact).apply();
        page = 0;
        show(Screen.BOOKS);
      });
      pager.addView(density, new LinearLayout.LayoutParams(dp(56), -1));
    }
  }

  // turn moves the list by one whole page and reports whether it moved.
  private boolean turn(int by) {
    int was = page;
    page += by;
    fill();
    return page != was;
  }

  // paged sets the rows the list pages through, each dpTall tall.
  private void paged(List<Supplier<View>> made, int dpTall) {
    rows.clear();
    rows.addAll(made);
    rowDp = dpTall;
  }

  private void header() {
    TextView title = text("ink-drop", 34, INK);
    title.setTypeface(Typeface.SERIF, Typeface.BOLD);
    body.addView(title);
    body.addView(text("Pages and feeds, kept for reading offline.", 17, GREY), gap(2));
  }

  private void tabs() {
    LinearLayout row = new LinearLayout(this);
    row.addView(tab("Books", Screen.BOOKS));
    row.addView(tab("Feeds  " + feeds.list.size(), Screen.FEEDS));
    row.addView(tab("Logs", Screen.LOGS));
    row.addView(tab("Settings", Screen.SETTINGS));
    body.addView(row, gap(28));
    body.addView(rule(INK), line(0));
  }

  private View tab(String label, Screen s) {
    boolean on = screen == s;
    TextView t = text(label, 20, on ? INK : GREY);
    t.setTypeface(Typeface.DEFAULT, on ? Typeface.BOLD : Typeface.NORMAL);
    t.setPadding(0, dp(8), 0, dp(14));
    // The bar under the chosen tab: a black layer showing 4dp below a white one.
    LayerDrawable bar = new LayerDrawable(new Drawable[] {new ColorDrawable(on ? INK : PAPER), new ColorDrawable(PAPER)});
    bar.setLayerInset(1, 0, 0, 0, dp(4));
    t.setBackground(bar);
    t.setOnClickListener(v -> show(s));
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-2, -2);
    p.rightMargin = dp(40);
    t.setLayoutParams(p);
    return t;
  }

  private void booksScreen() {
    if (folder() == null) {
      body.addView(text("Choose where your books go. The tablet's library reads them from there.", 19, INK), gap(32));
      body.addView(button("Choose a folder", true, v -> pickFolder()), gap(20));
      return;
    }
    int n = feeds.list.size();
    TextView refresh = n == 0
        ? button("Add your first feed", true, v -> show(Screen.ADD))
        : button(busy ? "Working…" : "Refresh " + n + (n == 1 ? " feed" : " feeds"), true, v -> refreshFeeds());
    body.addView(refresh, gap(28));
    TextView hint = text(status.isEmpty() ? "To save a page, share it to ink-drop from the browser." : status, 17, status.isEmpty() ? GREY : INK);
    body.addView(hint, gap(12));

    if (shelfCache == null) {
      load();
      body.addView(text("Reading your books…", 17, GREY), gap(24));
        return;
    }
    List<String[]> all = unreadable ? null : shelfCache;
    // recent is every book Today keeps; the section chips count those.
    List<String[]> recent = new ArrayList<>(), books = new ArrayList<>();
    long dayAgo = System.currentTimeMillis() - DateUtils.DAY_IN_MILLIS;
    if (all != null) {
      for (String[] b : all) if (!today || Long.parseLong(b[6]) >= dayAgo) recent.add(b);
      for (String[] b : recent) {
        if ((filter == null || Refresher.sameShelf(filter, b[4])) && (section == null || section.equals(b[5]))) books.add(b);
      }
    }
    if (all != null && !all.isEmpty()) shelves(all);
    if (all != null && filter != null) sections(recent);
    if (all == null) {
      body.addView(text("This folder can no longer be read. Choose Books/ink-drop again.", 18, INK), gap(32));
      body.addView(button("Choose the folder", true, v -> pickFolder()), gap(16));
    } else if (books.isEmpty()) {
      String empty = today ? "Nothing arrived" + (filter == null ? "" : " in " + filter) + " in the last 24 hours."
          : filter == null ? "No books yet. Refresh your feeds, or share a page." : "Nothing in " + filter + " yet.";
      body.addView(text(empty, 18, GREY), gap(32));
    } else {
      body.addView(rule(INK), line(20));
      List<Supplier<View>> made = new ArrayList<>();
      for (String[] b : books) made.add(() -> bookRow(b));
      paged(made, prefs.getBoolean("compact", false) ? 44 : 80);
    }
  }

  // shelves offers Today, then one chip per folder, newest first, after All
  // and Pages, so the list shows one feed's articles at a time.
  private void shelves(List<String[]> all) {
    List<String> names = new ArrayList<>();
    for (String[] b : all) if (!b[4].isEmpty() && !names.contains(b[4])) names.add(b[4]);
    for (String n : new ArrayList<>(names)) {
      if (n.equalsIgnoreCase("Laptop")) {
        names.remove(n);
        names.add(0, n);
      }
    }
    if (names.remove("Pages")) names.add(0, "Pages");
    if (names.remove(Refresher.READ_LATER)) names.add(0, Refresher.READ_LATER);
    if (names.remove(Refresher.FAVOURITES)) names.add(0, Refresher.FAVOURITES);
    names.add(0, null);
    LinearLayout line = new LinearLayout(this);
    body.addView(line, gap(28));
    int used = 9, lines = 1;
    LinearLayout.LayoutParams after = new LinearLayout.LayoutParams(-2, -2);
    after.rightMargin = dp(22);
    line.addView(chip("Today", today, v -> {
      today = !today;
      page = 0;
      show(Screen.BOOKS);
    }), after);
    for (int i = 0; i < names.size(); i++) {
      String n = names.get(i);
      String label = n == null ? "All" : n;
      if (used + label.length() > 90) {
        // Two rows at most, so the list keeps its room: the rest of the
        // folders open from the Feeds tab.
        if (lines == 2) {
          int left = names.size() - i;
          TextView more = text("+" + left + " more  ›", 16, INK);
          more.setPadding(dp(16), dp(10), dp(16), dp(10));
          more.setBackground(box(PAPER, GREY, 1, 20));
          more.setOnClickListener(v -> show(Screen.FEEDS));
          line.addView(more);
          return;
        }
        body.addView(line = new LinearLayout(this), gap(10));
        used = 0;
        lines++;
      }
      boolean on = n == null ? filter == null : Refresher.sameShelf(n, filter);
      LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-2, -2);
      p.rightMargin = dp(10);
      TextView c = chip(label, on, v -> {
        filter = n;
        section = null;
        page = 0;
        show(Screen.BOOKS);
      });
      // A long press deletes the folder's books, and so its chip; Laptop is
      // the laptop's sync's and Pages holds the pages shared, each its own.
      if (n != null && !n.equalsIgnoreCase("Laptop") && !n.equalsIgnoreCase("Pages") && !Refresher.kept(n)) {
        c.setOnLongClickListener(v -> {
          deleteFolder(n, all);
          return true;
        });
      }
      line.addView(c, p);
      used += label.length() + 4;
    }
  }

  // deleteFolder deletes every book in a folder of the Books tab after asking,
  // since nothing brings them back. A feed still listed fills it again.
  private void deleteFolder(String shelf, List<String[]> all) {
    int n = 0;
    for (String[] b : all) if (Refresher.sameShelf(shelf, b[4])) n++;
    String books = n + (n == 1 ? " book" : " books");
    String feed = null;
    for (Feeds.Feed f : feeds.list) if (Refresher.sameShelf(Refresher.shelf(f.title), shelf)) feed = f.title;
    new AlertDialog.Builder(this)
        .setTitle("Delete the " + shelf + " folder?")
        .setMessage("Its " + books + " leave the tablet and cannot be brought back."
            + (feed == null ? "" : " The " + feed + " feed fills it again at its next refresh: remove the feed on the Feeds tab to stop it."))
        .setPositiveButton("Delete " + books, (d, w) -> {
          setStatus("Deleting " + books + "…");
          new Thread(() -> {
            boolean gone = refresher.deleteShelf(shelf);
            runOnUiThread(() -> {
              status = gone ? "Deleted the " + shelf + " folder." : "Some books in " + shelf + " could not be deleted: see Logs.";
              if (Refresher.sameShelf(filter, shelf)) filter = null;
              section = null;
              shelfCache = null;
              show(Screen.BOOKS);
            });
          }).start();
        })
        .setNegativeButton("Cancel", null)
        .show();
  }

  // sections offers, under the chosen folder, one smaller chip per section
  // folder inside it, the fullest first, when it holds more than one.
  private void sections(List<String[]> all) {
    java.util.Map<String, Integer> count = new java.util.HashMap<>();
    for (String[] b : all) if (Refresher.sameShelf(filter, b[4]) && !b[5].isEmpty()) count.merge(b[5], 1, Integer::sum);
    if (count.size() < 2) return;
    List<String> names = new ArrayList<>(count.keySet());
    names.sort((a, b) -> count.get(b) - count.get(a) != 0 ? count.get(b) - count.get(a) : a.compareTo(b));
    names.add(0, null);
    LinearLayout line = null;
    int used = 0, lines = 0;
    for (String n : names) {
      String label = n == null ? "Every section" : n + "  " + count.get(n);
      if (line == null || used + label.length() > 100) {
        // Two rows at most, the fullest sections first, so the list keeps its room.
        if (lines == 2) return;
        body.addView(line = new LinearLayout(this), gap(used == 0 ? 14 : 8));
        used = 0;
        lines++;
      }
      boolean on = n == null ? section == null : n.equals(section);
      TextView chip = text(label, 14, on ? PAPER : INK);
      chip.setPadding(dp(12), dp(6), dp(12), dp(6));
      chip.setBackground(box(on ? GREY : PAPER, GREY, 1, 14));
      chip.setOnClickListener(v -> {
        section = n;
        page = 0;
        show(Screen.BOOKS);
      });
      LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-2, -2);
      p.rightMargin = dp(8);
      line.addView(chip, p);
      used += label.length() + 3;
    }
  }

  // settingsScreen holds what applies to every feed and book: the refresh on
  // its own, the pictures, and the folder the books go to.
  private void settingsScreen() {
    if (!status.isEmpty()) body.addView(text(status, 17, INK), gap(20));
    autoRefresh();
    // Grey is the lightest on a greyscale screen; either way no picture is
    // wider than the screen.
    choices("Pictures in new books", new String[] {"Grey", "Colour"}, new int[] {0, 1}, prefs.getBoolean("colour", false) ? 1 : 0,
        v -> prefs.edit().putBoolean("colour", v == 1).apply());
    LinearLayout row = new LinearLayout(this);
    row.setGravity(Gravity.CENTER_VERTICAL);
    row.addView(label("Books folder"), new LinearLayout.LayoutParams(dp(230), -2));
    String id = folder() == null ? "" : DocumentsContract.getTreeDocumentId(folder());
    row.addView(text(id.isEmpty() ? "None chosen" : id.substring(id.indexOf(':') + 1), 16, INK), new LinearLayout.LayoutParams(0, -2, 1));
    row.addView(link("Change", v -> pickFolder()));
    body.addView(row, gap(14));
  }

  // logsScreen shows what each run did, newest first: every stage's exit, the
  // errors it printed, and each book copied.
  private void logsScreen() {
    LinearLayout actions = new LinearLayout(this);
    actions.setGravity(Gravity.CENTER_VERTICAL);
    actions.addView(text("What ink-drop did, newest first.", 17, GREY), new LinearLayout.LayoutParams(0, -2, 1));
    actions.addView(button("Clear", false, v -> confirm("Clear the log?", "What ink-drop did so far leaves the Logs tab.", "Clear", () -> {
      refresher.clearLog();
      show(Screen.LOGS);
    })));
    body.addView(actions, gap(28));
    List<String> lines = refresher.logLines();
    if (lines.isEmpty()) {
      body.addView(text("Nothing logged yet. Refresh your feeds or share a page.", 18, GREY), gap(32));
      return;
    }
    body.addView(rule(INK), line(24));
    List<Supplier<View>> made = new ArrayList<>();
    for (int i = lines.size() - 1; i >= 0; i--) {
      String l = lines.get(i);
      made.add(() -> {
        boolean head = l.startsWith("── ");
        TextView t = text(head ? l.substring(3) : l, head ? 16 : 14, head ? INK : GREY);
        t.setTypeface(head ? Typeface.DEFAULT_BOLD : Typeface.MONOSPACE);
        t.setGravity(Gravity.BOTTOM);
        t.setMaxLines(2);
        t.setEllipsize(TextUtils.TruncateAt.END);
        t.setPadding(0, 0, 0, dp(4));
        return t;
      });
    }
    paged(made, 48);
  }

  // bookRow shows where a book comes from first: a mark for its section and its
  // feed's initials in the box, then the feed, the section and the book's age
  // above the title. A compact row says as much at the same sizes, in less
  // height: the initials beside the mark, and one line of title, not two.
  private View bookRow(String[] b) {
    boolean compact = prefs.getBoolean("compact", false);
    LinearLayout row = new LinearLayout(this);
    row.setGravity(Gravity.CENTER_VERTICAL);
    row.setPadding(0, dp(4), 0, dp(4));
    String title = title(b[0], b[1]);

    LinearLayout cover = new LinearLayout(this);
    cover.setOrientation(compact ? LinearLayout.HORIZONTAL : LinearLayout.VERTICAL);
    cover.setGravity(Gravity.CENTER);
    if (!compact) cover.setBackground(box(PALE, INK, 1, 3));
    TextView mark = text(mark(b[4], b[5]), compact ? 22 : 28, INK);
    mark.setGravity(Gravity.CENTER);
    cover.addView(mark);
    String initials = initials(b[4]);
    if (!initials.isEmpty()) {
      TextView who = text(initials, compact ? 13 : 12, INK);
      who.setTypeface(Typeface.DEFAULT_BOLD);
      who.setLetterSpacing(0.08f);
      who.setGravity(Gravity.CENTER);
      LinearLayout.LayoutParams beside = new LinearLayout.LayoutParams(-2, -2);
      if (compact) beside.leftMargin = dp(6);
      cover.addView(who, beside);
    }
    row.addView(cover, new LinearLayout.LayoutParams(dp(compact ? 72 : 58), dp(compact ? 36 : 68)));

    LinearLayout words = new LinearLayout(this);
    words.setOrientation(LinearLayout.VERTICAL);
    words.setPadding(dp(12), 0, 0, 0);
    LinearLayout top = new LinearLayout(this);
    top.setGravity(Gravity.CENTER_VERTICAL);
    java.util.Set<String> opened = prefs.getStringSet("opened", java.util.Set.of());
    if (Long.parseLong(b[3]) > newSince && !opened.contains(b[0])) {
      TextView tag = text("NEW", 11, PAPER);
      tag.setTypeface(Typeface.DEFAULT_BOLD);
      tag.setLetterSpacing(0.1f);
      tag.setPadding(dp(5), dp(1), dp(5), dp(1));
      tag.setBackground(box(INK, INK, 1, 3));
      LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-2, -2);
      p.rightMargin = dp(8);
      top.addView(tag, p);
    }
    TextView name = text(title, 17, INK);
    name.setTypeface(Typeface.SERIF);
    name.setMaxLines(compact ? 1 : 2);
    name.setEllipsize(TextUtils.TruncateAt.END);
    String age = DateUtils.getRelativeTimeSpanString(Long.parseLong(b[6]), System.currentTimeMillis(), DateUtils.MINUTE_IN_MILLIS).toString();
    String from = (b[4].isEmpty() ? "ink-drop" : b[4]) + (b[5].isEmpty() ? "" : "  ›  " + b[5]);
    String said = from.toUpperCase(Locale.getDefault()) + "  ·  " + age + (b[7].isEmpty() ? "" : "  ·  " + b[7]);
    TextView source = text(said, 12, compact ? GREY : INK);
    source.setTypeface(Typeface.DEFAULT_BOLD);
    source.setLetterSpacing(0.06f);
    source.setMaxLines(1);
    source.setEllipsize(TextUtils.TruncateAt.END);
    if (compact) {
      // One line: the title, then what the normal row says above it.
      top.addView(name, new LinearLayout.LayoutParams(0, -2, 1));
      source.setMaxWidth(dp(300));
      LinearLayout.LayoutParams right = new LinearLayout.LayoutParams(-2, -2);
      right.leftMargin = dp(12);
      top.addView(source, right);
      words.addView(top);
    } else {
      top.addView(source, new LinearLayout.LayoutParams(0, -2, 1));
      words.addView(top);
      words.addView(name, gap(2));
    }
    boolean pdf = b[1].endsWith(".pdf");
    row.addView(words, new LinearLayout.LayoutParams(0, -2, 1));
    // Later sets the book aside in Read later and ☆ moves it to Favourites, both
    // out of every feed's settings; a favourite stays one.
    if (!Refresher.underLaptop(b[0])) {
      boolean fav = Refresher.FAVOURITES.equalsIgnoreCase(b[4]), later = Refresher.READ_LATER.equalsIgnoreCase(b[4]);
      LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-2, -2);
      lp.leftMargin = dp(8);
      if (!fav && !later) row.addView(pill("Later", v -> keep(b[0], title, Refresher.READ_LATER)), lp);
      // Done, and the star of a favourite, put the book back where it came from.
      if (later) row.addView(pill("Done", v -> release(b[0], title)), lp);
      row.addView(icon(fav ? "★" : "☆", v -> {
        if (fav) release(b[0], title);
        else keep(b[0], title, Refresher.FAVOURITES);
      }), new LinearLayout.LayoutParams(dp(44), -1));
    }

    Uri doc = DocumentsContract.buildDocumentUriUsingTree(folder(), b[0]);
    // Laptop is Syncthing's: a book deleted there is a change sync would undo.
    if (!Refresher.underLaptop(b[0])) {
      row.setOnLongClickListener(v -> {
        deleteBook(b[0], title);
        return true;
      });
    }
    row.setOnClickListener(v -> {
      Intent open = new Intent(Intent.ACTION_VIEW);
      open.setDataAndType(doc, pdf ? "application/pdf" : "application/epub+zip");
      open.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
      try {
        away = true;
        startActivity(open);
        java.util.Set<String> read = new java.util.HashSet<>(prefs.getStringSet("opened", java.util.Set.of()));
        read.add(b[0]);
        prefs.edit().putStringSet("opened", read).apply();
      } catch (RuntimeException e) {
        away = false;
        setStatus("No reader on this tablet opens this book.");
      }
    });
    LinearLayout wrap = new LinearLayout(this);
    wrap.setOrientation(LinearLayout.VERTICAL);
    wrap.addView(row);
    wrap.addView(rule(PALE), line(0));
    return wrap;
  }

  // Marks for the sections news sites name in their addresses, keyed without
  // accents; a section with none takes the newspaper.
  private static final java.util.Map<String, String> MARKS = java.util.Map.ofEntries(
      java.util.Map.entry("politique", "🏛"), java.util.Map.entry("politics", "🏛"),
      java.util.Map.entry("sport", "⚽"), java.util.Map.entry("sports", "⚽"),
      java.util.Map.entry("monde", "🌍"), java.util.Map.entry("international", "🌍"), java.util.Map.entry("world", "🌍"),
      java.util.Map.entry("france", "📍"), java.util.Map.entry("societe", "👥"), java.util.Map.entry("society", "👥"),
      java.util.Map.entry("culture", "🎭"), java.util.Map.entry("economie", "💶"), java.util.Map.entry("economy", "💶"),
      java.util.Map.entry("business", "💶"), java.util.Map.entry("sciences", "🔬"), java.util.Map.entry("science", "🔬"),
      java.util.Map.entry("environnement", "🌿"), java.util.Map.entry("planete", "🌿"), java.util.Map.entry("environment", "🌿"),
      java.util.Map.entry("faits divers", "🚨"), java.util.Map.entry("idees", "💡"), java.util.Map.entry("opinion", "💡"),
      java.util.Map.entry("campus", "🎓"), java.util.Map.entry("education", "🎓"), java.util.Map.entry("sante", "🩺"),
      java.util.Map.entry("health", "🩺"), java.util.Map.entry("vrai ou fake", "🔎"), java.util.Map.entry("replay magazine", "📺"),
      java.util.Map.entry("intimites", "💬"), java.util.Map.entry("tech", "💻"), java.util.Map.entry("technology", "💻"),
      java.util.Map.entry("pixels", "💻"), java.util.Map.entry("cinema", "🎬"), java.util.Map.entry("musique", "🎵"),
      java.util.Map.entry("livres", "📚"), java.util.Map.entry("books", "📚"), java.util.Map.entry("justice", "⚖"),
      java.util.Map.entry("meteo", "☁"), java.util.Map.entry("voyage", "✈"), java.util.Map.entry("travel", "✈"),
      java.util.Map.entry("space", "🚀"), java.util.Map.entry("espace", "🚀"));

  // mark returns the symbol for a book's source: its section's, a page saved
  // from the browser, a book from the laptop, else the newspaper.
  static String mark(String shelf, String section) {
    String key = java.text.Normalizer.normalize(section.toLowerCase(Locale.ROOT), java.text.Normalizer.Form.NFD).replaceAll("\\p{M}", "");
    if (MARKS.containsKey(key)) return MARKS.get(key);
    if (shelf.equalsIgnoreCase(Refresher.FAVOURITES)) return "★";
    if (shelf.equalsIgnoreCase(Refresher.READ_LATER)) return "◔";
    if (shelf.equalsIgnoreCase("Pages")) return "📄";
    if (shelf.equalsIgnoreCase("Laptop")) return "💻";
    return "📰";
  }

  // initials returns a feed's two letters, one per word or the first two of a
  // single word: "Le Monde.fr" is LM, "Franceinfo" FR; Pages and Laptop have none.
  static String initials(String shelf) {
    if (shelf.isEmpty() || shelf.equalsIgnoreCase("Pages") || shelf.equalsIgnoreCase("Laptop") || Refresher.kept(shelf)) return "";
    String[] words = shelf.replaceAll("[^\\p{L}\\p{N} ]", " ").trim().split("\\s+");
    String out = words.length > 1 ? words[0].substring(0, 1) + words[1].substring(0, 1) : words[0].substring(0, Math.min(2, words[0].length()));
    return out.toUpperCase(Locale.ROOT);
  }

  private void feedsScreen() {
    LinearLayout actions = new LinearLayout(this);
    actions.addView(button("Add a feed", true, v -> show(Screen.ADD)), new LinearLayout.LayoutParams(0, -2, 1));
    actions.addView(space(12));
    actions.addView(button("Import", false, v -> importFeeds()));
    actions.addView(space(12));
    actions.addView(button("Export", false, v -> exportFeeds()));
    body.addView(actions, gap(28));
    if (!status.isEmpty()) body.addView(text(status, 17, INK), gap(12));

    if (feeds.list.isEmpty()) {
      body.addView(text("No feeds yet. Add one by its address, find one by topic, or import a list from another reader.", 18, GREY), gap(32));
      return;
    }
    body.addView(label("Your feeds"), gap(32));
    body.addView(rule(INK), line(8));
    List<Supplier<View>> made = new ArrayList<>();
    for (Feeds.Feed f : feeds.list) made.add(() -> feedRow(f));
    paged(made, 92);
  }

  private View feedRow(Feeds.Feed f) {
    {
      LinearLayout row = new LinearLayout(this);
      row.setGravity(Gravity.CENTER_VERTICAL);
      LinearLayout words = new LinearLayout(this);
      words.setOrientation(LinearLayout.VERTICAL);
      TextView name = text(f.title, 20, INK);
      name.setTypeface(Typeface.SERIF);
      name.setMaxLines(1);
      name.setEllipsize(TextUtils.TruncateAt.END);
      words.addView(name);
      TextView what = text(Feeds.host(f.url) + "  ·  " + keeps(f) + "  ›", 15, GREY);
      what.setMaxLines(1);
      what.setEllipsize(TextUtils.TruncateAt.END);
      words.addView(what, gap(4));
      row.addView(words, new LinearLayout.LayoutParams(0, -2, 1));
      // A tap opens what the feed keeps, and its books from there.
      words.setOnClickListener(v -> {
        editing = f.url;
        status = "";
        show(Screen.FEED);
      });
      row.addView(link("Remove", v -> removeFeed(f)));
      LinearLayout wrap = new LinearLayout(this);
      wrap.setOrientation(LinearLayout.VERTICAL);
      wrap.addView(row, new LinearLayout.LayoutParams(-1, 0, 1));
      wrap.addView(rule(PALE), line(0));
      return wrap;
    }
  }

  // removeFeed takes the feed off the list, and offers to delete the books in
  // its folder too, unless another feed files into the same folder; Laptop
  // and Pages are never a feed's folder. The folder is counted and deleted
  // off the screen's thread.
  private void removeFeed(Feeds.Feed f) {
    String shelf = Refresher.shelf(f.title);
    String other = null;
    // Shared storage folds case: "World news" and "World News" are one folder.
    for (Feeds.Feed g : feeds.list) if (!g.url.equals(f.url) && Refresher.sameShelf(Refresher.shelf(g.title), shelf)) other = g.title;
    Runnable drop = () -> {
      feeds.remove(f.url);
      // The books stay, so the record of the entries they hold stays too: the
      // feed added again later brings none of them twice.
      new Thread(() -> refresher.dropWork(f.url, f.title, true)).start();
      saveFeeds("Removed " + f.title + ".");
      show(Screen.FEEDS);
    };
    if (other != null) {
      String sharing = other;
      new AlertDialog.Builder(this)
          .setTitle("Remove " + f.title + "?")
          .setMessage("Its books stay in the " + shelf + " folder, which also holds the books of " + sharing + ", and are kept as long as " + sharing + " keeps its own.")
          .setPositiveButton("Remove the feed", (d, w) -> drop.run())
          .setNegativeButton("Cancel", null)
          .show();
      return;
    }
    new Thread(() -> {
      List<String[]> all = books();
      int n = 0;
      if (all != null) for (String[] b : all) if (Refresher.sameShelf(shelf, b[4])) n++;
      int count = n;
      runOnUiThread(() -> {
        if (isFinishing() || isDestroyed()) return;
        if (count == 0) {
          confirm("Remove " + f.title + "?", "It leaves your feeds; its folder holds no books.", "Remove", drop);
          return;
        }
        String books = count + (count == 1 ? " book" : " books");
        new AlertDialog.Builder(this)
            .setTitle("Remove " + f.title + "?")
            .setMessage("Its " + books + " in the " + shelf + " folder can go too. Deleted books cannot be brought back.")
            .setPositiveButton("Remove and delete " + books, (d, w) -> {
              feeds.remove(f.url);
              saveFeeds("");
              setStatus("Deleting " + books + "…");
              new Thread(() -> {
                refresher.dropWork(f.url, f.title, false);
                boolean gone = refresher.deleteShelf(shelf);
                runOnUiThread(() -> {
                  saveFeeds(gone ? "Removed " + f.title + " and its " + books + "." : "Removed " + f.title + ". Its books could not be deleted: see Logs.");
                  shelfCache = null;
                  show(Screen.FEEDS);
                });
              }).start();
            })
            .setNeutralButton("Keep the books", (d, w) -> drop.run())
            .setNegativeButton("Cancel", null)
            .show();
      });
    }).start();
  }

  // keeps says in a few words what a feed keeps, for its row.
  static String keeps(Feeds.Feed f) {
    int d = f.keepDays;
    String s = "keeps " + (d == 0 ? "for ever" : d == 1 ? "1 day" : d == 7 ? "1 week" : d == 30 ? "1 month" : d + " days");
    if (f.keepMost > 0) s += ", at most " + f.keepMost;
    if (f.together) s += ", articles together";
    if (!f.mute.isEmpty() || !f.only.isEmpty()) s += ", words picked";
    return s;
  }

  // feedScreen shows one feed and what it keeps, each choice saved as it is
  // made and used from the next refresh on.
  private void feedScreen() {
    Feeds.Feed f = feeds.get(editing);
    String shelf = Refresher.shelf(f.title);
    body.addView(link("‹  Back to feeds", v -> show(Screen.FEEDS)));
    TextView title = text(f.title, 30, INK);
    title.setTypeface(Typeface.SERIF, Typeface.BOLD);
    title.setMaxLines(1);
    title.setEllipsize(TextUtils.TruncateAt.END);
    body.addView(title, gap(16));
    body.addView(text(Feeds.host(f.url), 15, GREY), gap(4));
    body.addView(button("Open its books", true, v -> {
      filter = shelf;
      section = null;
      page = 0;
      show(Screen.BOOKS);
    }), gap(20));
    String[] dayWords = {"1 day", "3 days", "1 week", "1 month", "For ever"};
    int[] dayValues = {1, 3, 7, 30, 0};
    choices("Keep each book", dayWords, dayValues, f.keepDays, v -> {
      if (v == 0) {
        change(g -> g.keepDays = v);
        return;
      }
      String words = dayWords[java.util.Arrays.stream(dayValues).boxed().toList().indexOf(v)].toLowerCase(Locale.ROOT);
      confirm("Keep each book " + words + "?", "From the next refresh on, the books older than " + words + " are deleted from the " + shelf + " folder.", "Keep " + words,
          () -> {
            change(g -> g.keepDays = v);
            show(Screen.FEED);
          });
    });
    choices("Keep at most", new String[] {"No limit", "20 books", "50 books", "100 books"}, new int[] {0, 20, 50, 100}, f.keepMost, v -> {
      if (v == 0) {
        change(g -> g.keepMost = v);
        return;
      }
      confirm("Keep at most " + v + " books?", "From the next refresh on, the books past the newest " + v + " are deleted from the " + shelf + " folder.", "Keep " + v,
          () -> {
            change(g -> g.keepMost = v);
            show(Screen.FEED);
          });
    });
    body.addView(text("After each refresh, the books past these leave the " + shelf + " folder.", 15, GREY), gap(8));
    choices("New articles arrive as", new String[] {"Separate books", "One book together"}, new int[] {0, 1}, f.together ? 1 : 0,
        v -> change(g -> g.together = v == 1));
    words("Leave out articles holding", "Words, comma-separated: horoscope, football", f.mute, w -> change(g -> g.mute = w));
    words("Keep only articles holding", "Words, comma-separated; empty keeps every article", f.only, w -> change(g -> g.only = w));
    body.addView(text("A word matches a whole word of the title or the summary the feed gives, whatever its case and accents. Words count once you leave the field, and an article left out never comes back.", 15, GREY), gap(8));
    body.addView(link("Remove this feed", v -> removeFeed(f)), gap(16));
  }

  // change applies a change to the feed whose settings show and saves the list;
  // the list read again since the screen was drawn is the one changed.
  private void change(java.util.function.Consumer<Feeds.Feed> c) {
    Feeds.Feed f = feeds.get(editing);
    if (f == null) return;
    c.accept(f);
    saveFeeds("");
  }

  // confirm asks before anything is deleted, and runs yes only on its button.
  private void confirm(String title, String message, String button, Runnable yes) {
    new AlertDialog.Builder(this)
        .setTitle(title)
        .setMessage(message)
        .setPositiveButton(button, (d, w) -> yes.run())
        .setNegativeButton("Cancel", null)
        .show();
  }

  // words adds a labelled field whose words go to set once typed: on Done,
  // when the field loses focus, or when the screen changes. A refresh running
  // meanwhile never sees a word half typed, "f" of "football".
  private void words(String title, String hint, String now, java.util.function.Consumer<String> set) {
    LinearLayout line = new LinearLayout(this);
    line.setGravity(Gravity.CENTER_VERTICAL);
    line.addView(label(title), new LinearLayout.LayoutParams(dp(230), -2));
    EditText e = field(hint, EditorInfo.IME_ACTION_DONE);
    e.setText(now);
    // The words last saved, so typing them back after a save is a change too.
    String[] saved = {now};
    Runnable commit = () -> {
      String w = e.getText().toString().trim();
      if (!w.equals(saved[0])) {
        set.accept(w);
        saved[0] = w;
      }
    };
    leaving.add(commit);
    e.setOnFocusChangeListener((v, has) -> {
      if (!has) commit.run();
    });
    e.setOnEditorActionListener((v, a, ev) -> {
      commit.run();
      hideKeyboard();
      e.clearFocus();
      return true;
    });
    line.addView(e, new LinearLayout.LayoutParams(0, -2, 1));
    body.addView(line, gap(14));
  }

  // choices adds a label and one chip per value, the chosen one filled; a tap
  // passes its value to pick and draws the screen again.
  private void choices(String title, String[] labels, int[] values, int chosen, java.util.function.IntConsumer pick) {
    // The label and its chips on one line.
    LinearLayout row = new LinearLayout(this);
    row.setGravity(Gravity.CENTER_VERTICAL);
    row.addView(label(title), new LinearLayout.LayoutParams(dp(230), -2));
    for (int i = 0; i < labels.length; i++) {
      int v = values[i];
      LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-2, -2);
      p.rightMargin = dp(10);
      row.addView(chip(labels[i], v == chosen, x -> {
        pick.accept(v);
        show(screen);
      }), p);
    }
    body.addView(row, gap(14));
  }

  // autoRefresh offers how often the feeds refresh on their own, and says
  // when the last refresh ran.
  private void autoRefresh() {
    String[] labels = new String[RefreshJob.HOURS.length];
    for (int i = 0; i < labels.length; i++) labels[i] = RefreshJob.HOURS[i] == 0 ? "Off" : "Every " + RefreshJob.HOURS[i] + " h";
    choices("Refresh on its own", labels, RefreshJob.HOURS, prefs.getInt("auto-hours", 3), h -> {
      prefs.edit().putInt("auto-hours", h).apply();
      RefreshJob.schedule(this, h);
    });
    long last = prefs.getLong("last-run", 0);
    String when = last == 0 ? "No refresh has run yet." : "Last refresh "
        + DateUtils.getRelativeTimeSpanString(last, System.currentTimeMillis(), DateUtils.MINUTE_IN_MILLIS)
        + ": " + prefs.getString("last-result", "");
    body.addView(text(when + " It runs only while the tablet is online.", 15, GREY), gap(8));
  }

  private void addScreen() {
    body.addView(link("‹  Back to feeds", v -> show(Screen.FEEDS)));
    TextView title = text("Add a feed", 30, INK);
    title.setTypeface(Typeface.SERIF, Typeface.BOLD);
    body.addView(title, gap(16));

    body.addView(label("By its address"), gap(28));
    LinearLayout pasteRow = new LinearLayout(this);
    pasteRow.setGravity(Gravity.CENTER_VERTICAL);
    EditText paste = field("A site or a feed, like go.dev/blog", EditorInfo.IME_ACTION_DONE);
    paste.setInputType(EditorInfo.TYPE_CLASS_TEXT | EditorInfo.TYPE_TEXT_VARIATION_URI);
    pasteRow.addView(paste, new LinearLayout.LayoutParams(0, -2, 1));
    pasteRow.addView(space(12));
    Runnable addPasted = () -> {
      String u = paste.getText().toString().trim();
      if (u.isEmpty()) return;
      if (!u.startsWith("http://") && !u.startsWith("https://")) u = "https://" + u;
      boolean added = feeds.add(new Feeds.Feed("", u, "", ""));
      saveFeeds(added ? "Added " + Feeds.host(u) + ". Refresh to fetch it." : "That feed is already in your list.");
      show(Screen.FEEDS);
    };
    pasteRow.addView(button("Add", true, v -> addPasted.run()));
    paste.setOnEditorActionListener((v, a, e) -> {
      addPasted.run();
      return true;
    });
    body.addView(pasteRow, gap(10));
    body.addView(text("A site's address works too: ink-drop finds the feed it links to.", 15, GREY), gap(8));

    body.addView(label("Or find one"), gap(36));
    EditText search = field("Search a topic or a name: space, cooking, France…", EditorInfo.IME_ACTION_SEARCH);
    body.addView(search, gap(10));
    TextView source = text(directory().size() + " feeds from the awesome-rss-feeds list, searched on this tablet.", 15, GREY);
    body.addView(source, gap(8));
    LinearLayout results = new LinearLayout(this);
    results.setOrientation(LinearLayout.VERTICAL);
    body.addView(results, gap(8));
    search.addTextChangedListener(new TextWatcher() {
      @Override public void beforeTextChanged(CharSequence s, int a, int b, int c) {}
      @Override public void onTextChanged(CharSequence s, int a, int b, int c) {}
      @Override public void afterTextChanged(Editable s) { results(results, s.toString(), search); }
    });
    search.setOnEditorActionListener((v, a, e) -> {
      hideKeyboard();
      return true;
    });
    results(results, "", search);
  }

  // results lists the topics while the search is empty, else the feeds whose
  // name, topic or description holds every word typed.
  private void results(LinearLayout into, String query, EditText search) {
    into.removeAllViews();
    page = 0;
    String[] words = query.toLowerCase(Locale.ROOT).trim().split("\\s+");
    if (query.trim().isEmpty()) {
      java.util.Map<String, Integer> count = new java.util.LinkedHashMap<>();
      for (Feeds.Feed f : directory()) count.merge(f.topic, 1, Integer::sum);
      into.addView(label("Topics and countries"));
      List<Supplier<View>> made = new ArrayList<>();
      for (java.util.Map.Entry<String, Integer> t : count.entrySet()) {
        made.add(() -> {
          LinearLayout row = new LinearLayout(this);
          row.setGravity(Gravity.CENTER_VERTICAL);
          TextView name = text(t.getKey(), 19, INK);
          name.setTypeface(Typeface.SERIF);
          row.addView(name, new LinearLayout.LayoutParams(0, -2, 1));
          row.addView(text(t.getValue() + " feeds  ›", 15, GREY));
          row.setOnClickListener(v -> search.setText(t.getKey()));
          LinearLayout wrap = new LinearLayout(this);
          wrap.setOrientation(LinearLayout.VERTICAL);
          wrap.addView(row, new LinearLayout.LayoutParams(-1, 0, 1));
          wrap.addView(rule(PALE), line(0));
          return wrap;
        });
      }
      paged(made, 60);
      fill();
      return;
    }
    List<Supplier<View>> made = new ArrayList<>();
    for (Feeds.Feed f : directory()) {
      String hay = (f.title + " " + f.topic + " " + f.about).toLowerCase(Locale.ROOT);
      boolean all = true;
      for (String w : words) if (!hay.contains(w)) all = false;
      if (all) made.add(() -> resultRow(f));
    }
    into.addView(text(made.isEmpty() ? "Nothing matches. Try a broader word, or paste the address above."
        : made.size() + (made.size() == 1 ? " feed matches." : " feeds match."), 16, GREY));
    paged(made, 124);
    fill();
  }

  private View resultRow(Feeds.Feed f) {
    LinearLayout row = new LinearLayout(this);
    row.setGravity(Gravity.CENTER_VERTICAL);
    LinearLayout words = new LinearLayout(this);
    words.setOrientation(LinearLayout.VERTICAL);
    TextView name = text(f.title, 19, INK);
    name.setTypeface(Typeface.SERIF);
    name.setMaxLines(1);
    name.setEllipsize(TextUtils.TruncateAt.END);
    words.addView(name);
    words.addView(text(f.topic + "  ·  " + Feeds.host(f.url), 14, GREY), gap(4));
    if (!f.about.isEmpty()) {
      TextView about = text(f.about, 15, GREY);
      about.setMaxLines(2);
      about.setEllipsize(TextUtils.TruncateAt.END);
      words.addView(about, gap(4));
    }
    row.addView(words, new LinearLayout.LayoutParams(0, -2, 1));
    row.addView(space(16));
    boolean has = feeds.has(f.url);
    TextView add = button(has ? "Added" : "Add", !has, null);
    add.setOnClickListener(v -> {
      if (feeds.has(f.url)) {
        // The folder is named by the list's title, an imported one included.
        String listed = f.title;
        for (Feeds.Feed g : feeds.list) if (g.url.equals(f.url)) listed = g.title;
        String title = listed;
        confirm("Remove " + title + "?", "It leaves your feeds; its books stay.", "Remove", () -> {
          feeds.remove(f.url);
          new Thread(() -> refresher.dropWork(f.url, title, true)).start();
          add.setText("Add");
          add.setTextColor(PAPER);
          add.setBackground(box(INK, INK, 2, 6));
          saveFeeds("");
        });
        return;
      } else {
        feeds.add(new Feeds.Feed(f.title, f.url, f.topic, f.about));
        add.setText("Added");
        add.setTextColor(INK);
        add.setBackground(box(PAPER, INK, 2, 6));
      }
      saveFeeds("");
    });
    row.addView(add);
    LinearLayout wrap = new LinearLayout(this);
    wrap.setOrientation(LinearLayout.VERTICAL);
    wrap.addView(row, new LinearLayout.LayoutParams(-1, 0, 1));
    wrap.addView(rule(PALE), line(0));
    return wrap;
  }

  // ---- Feeds and books ----

  private List<Feeds.Feed> directory() {
    if (directory != null) return directory;
    directory = new ArrayList<>();
    for (String dir : new String[] {"topics", "countries"}) {
      try {
        for (String name : getAssets().list("directory/" + dir)) {
          if (!name.endsWith(".opml")) continue;
          try (InputStream in = getAssets().open("directory/" + dir + "/" + name)) {
            directory.addAll(Feeds.read(in, name.replace(".opml", "")));
          }
        }
      } catch (Exception e) {
        log("directory " + dir + ": " + e);
      }
    }
    return directory;
  }

  private void saveFeeds(String message) {
    try {
      feeds.save();
      status = message;
    } catch (IOException e) {
      status = "Your feed list could not be saved.";
      log("save feeds: " + e);
    }
  }

  private void importFeeds() {
    Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT);
    i.addCategory(Intent.CATEGORY_OPENABLE);
    i.setType("*/*");
    away = true;
    startActivityForResult(i, IMPORT);
  }

  private void exportFeeds() {
    Intent i = new Intent(Intent.ACTION_CREATE_DOCUMENT);
    i.addCategory(Intent.CATEGORY_OPENABLE);
    i.setType("text/x-opml");
    i.putExtra(Intent.EXTRA_TITLE, "ink-drop-feeds.opml");
    away = true;
    startActivityForResult(i, EXPORT);
  }

  private void refreshFeeds() {
    List<Feeds.Feed> list = new ArrayList<>(feeds.list);
    run(list.isEmpty() ? null : "Fetching " + list.size() + (list.size() == 1 ? " feed…" : " feeds…"), () -> refresher.refresh(list, "asked"));
  }

  // books returns the books in the picked folder and in its folders, newest
  // first, or null when the folder cannot be read. Each is its document id,
  // file name, size, time, the top folder holding it and the section folder
  // inside that, "" for none.
  private List<String[]> books() {
    Uri tree = folder();
    List<String[]> books = new ArrayList<>();
    try {
      // A folder deleted since it was picked lists as empty rather than failing.
      String root = DocumentsContract.getTreeDocumentId(tree);
      try (Cursor c = getContentResolver().query(DocumentsContract.buildDocumentUriUsingTree(tree, root), new String[] {Document.COLUMN_DOCUMENT_ID}, null, null, null)) {
        if (c == null || !c.moveToFirst()) return null;
      }
      list(tree, root, "", "", 0, books);
    } catch (RuntimeException e) {
      log("list books: " + e);
      return null;
    }
    books.sort((a, b) -> Long.compare(Long.parseLong(b[6]), Long.parseLong(a[6])));
    return books;
  }

  // list walks at most four folders deep, since Laptop holds the laptop's own
  // folders, and skips hidden ones such as Syncthing's.
  private void list(Uri tree, String id, String shelf, String sec, int depth, List<String[]> into) {
    Uri children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, id);
    String[] cols = {Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_SIZE, Document.COLUMN_LAST_MODIFIED, Document.COLUMN_MIME_TYPE};
    List<String[]> dirs = new ArrayList<>();
    try (Cursor c = getContentResolver().query(children, cols, null, null, null)) {
      while (c != null && c.moveToNext()) {
        String name = c.getString(1);
        if (Document.MIME_TYPE_DIR.equals(c.getString(4))) {
          if (depth < 4 && !name.startsWith(".")) {
            dirs.add(new String[] {c.getString(0), shelf.isEmpty() ? name : shelf, shelf.isEmpty() ? "" : sec.isEmpty() ? name : sec});
          }
          continue;
        }
        if (!name.endsWith(".epub") && !name.endsWith(".pdf")) continue;
        String doc = c.getString(0);
        into.add(new String[] {doc, name, Long.toString(c.getLong(2)), Long.toString(c.getLong(3)), shelf, sec,
            Long.toString(refresher.published(doc, c.getLong(3))), refresher.author(doc)});
      }
    }
    for (String[] d : dirs) list(tree, d[0], d[1], d[2], depth + 1, into);
  }

  private String title(String doc, String file) {
    return refresher.title(doc, file);
  }

  // The books last read from the folder, null until read again; the walk runs
  // off the screen's thread, since a library grows to thousands of books.
  private List<String[]> shelfCache;
  private boolean unreadable, loading;

  private void load() {
    if (loading) return;
    loading = true;
    new Thread(() -> {
      long before = Refresher.runs();
      List<String[]> read = books();
      if (read != null) {
        java.util.Set<String> docs = new java.util.HashSet<>();
        for (String[] b : read) docs.add(b[0]);
        refresher.forgetTitles(docs, before);
      }
      runOnUiThread(() -> {
        loading = false;
        unreadable = read == null;
        shelfCache = read == null ? new ArrayList<>() : read;
        if (screen == Screen.BOOKS) show(Screen.BOOKS);
      });
    }).start();
  }

  // keep moves a book to Favourites or Read later, where it stays until
  // deleted by hand.
  private void keep(String doc, String title, String into) {
    setStatus("Moving “" + title + "” to " + into + "…");
    new Thread(() -> {
      boolean moved = refresher.keep(doc, into);
      runOnUiThread(() -> {
        status = moved ? "“" + title + "” is in " + into + "." : "That book could not be moved: see Logs.";
        load();
      });
    }).start();
  }

  // release puts a book from Favourites or Read later back in its folder.
  private void release(String doc, String title) {
    setStatus("Putting “" + title + "” back…");
    new Thread(() -> {
      boolean moved = refresher.release(doc);
      runOnUiThread(() -> {
        status = moved ? "“" + title + "” is back in its folder." : "Its first folder is gone or not known: delete it with a long press, or keep it here.";
        load();
      });
    }).start();
  }

  // pill is a small word button on a row.
  private TextView pill(String word, View.OnClickListener click) {
    TextView t = text(word, 14, INK);
    t.setGravity(Gravity.CENTER);
    t.setPadding(dp(10), dp(4), dp(10), dp(4));
    t.setBackground(box(PAPER, INK, 1, 12));
    t.setOnClickListener(click);
    return t;
  }

  // icon is a symbol a tap acts on, or that only shows where click is null.
  private TextView icon(String symbol, View.OnClickListener click) {
    TextView t = text(symbol, 26, INK);
    t.setGravity(Gravity.CENTER);
    if (click != null) t.setOnClickListener(click);
    return t;
  }

  // deleteBook deletes one book after asking, since nothing brings it back.
  private void deleteBook(String doc, String title) {
    new AlertDialog.Builder(this)
        .setTitle("Delete “" + title + "”?")
        .setMessage("The book leaves the tablet. It cannot be brought back.")
        .setPositiveButton("Delete", (d, w) -> {
          boolean gone;
          try {
            gone = DocumentsContract.deleteDocument(getContentResolver(), DocumentsContract.buildDocumentUriUsingTree(folder(), doc));
          } catch (Exception e) {
            log("delete " + doc + ": " + e);
            gone = false;
          }
          log("deleted " + doc + ": " + gone);
          status = gone ? "Deleted “" + title + "”." : "That book could not be deleted: see Logs.";
          shelfCache = null;
          show(Screen.BOOKS);
        })
        .setNegativeButton("Cancel", null)
        .show();
  }

  // ---- Making books ----

  // make saves the page at url as a book in the Pages folder.
  private void make(String url) {
    filter = null;
    section = null;
    if (folder() == null) {
      setStatus("Choose a folder first.");
      return;
    }
    // A share always runs, after any run under way, and says it waits.
    setStatus(Refresher.busy() ? "Waiting for the refresh to finish, then saving the page…" : "Saving the page…");
    new Thread(() -> {
      String done = refresher.save(url, "asked");
      runOnUiThread(() -> {
        status = done;
        shelfCache = null;
        if (screen == Screen.BOOKS) show(Screen.BOOKS);
      });
    }).start();
  }

  // run does work on a background thread, saying what it does meanwhile and
  // what came of it after.
  private void run(String doing, java.util.function.Supplier<String> work) {
    if (busy || doing == null) return;
    if (folder() == null) {
      setStatus("Choose a folder first.");
      return;
    }
    busy = true;
    setStatus(doing);
    new Thread(() -> {
      String done = work.get();
      runOnUiThread(() -> {
        busy = false;
        status = done;
        shelfCache = null;
        if (screen == Screen.BOOKS) show(Screen.BOOKS);
      });
    }).start();
  }

  // ---- Files the user picks ----

  private Uri folder() {
    return refresher.folder();
  }

  // pickFolder opens the system picker at Books/ink-drop, the folder the app
  // shares with the laptop's sync.
  private void pickFolder() {
    Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);
    i.putExtra(DocumentsContract.EXTRA_INITIAL_URI, DocumentsContract.buildDocumentUri("com.android.externalstorage.documents", "primary:Books/ink-drop"));
    away = true;
    startActivityForResult(i, PICK_FOLDER);
  }

  @Override
  protected void onActivityResult(int request, int result, Intent data) {
    if (result != RESULT_OK || data == null || data.getData() == null) return;
    Uri uri = data.getData();
    if (request == PICK_FOLDER) {
      int both = Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION;
      // Syncthing's own folder is never the app's to write in.
      if (Refresher.underLaptop(DocumentsContract.getTreeDocumentId(uri))) {
        setStatus("That is the laptop's sync folder. Choose Books/ink-drop, the folder holding it.");
        return;
      }
      getContentResolver().takePersistableUriPermission(uri, both);
      // The app keeps access to the one folder it writes in, never the last one too.
      Uri old = folder();
      if (old != null && !old.equals(uri)) {
        try {
          getContentResolver().releasePersistableUriPermission(old, both);
        } catch (SecurityException e) {
          log("release " + old + ": " + e);
        }
      }
      prefs.edit().putString("folder", uri.toString()).apply();
      status = "";
      shelfCache = null;
      show(Screen.BOOKS);
    } else if (request == IMPORT) {
      int added = 0;
      try (InputStream in = getContentResolver().openInputStream(uri)) {
        for (Feeds.Feed f : Feeds.read(in, "")) if (feeds.add(f)) added++;
        saveFeeds(added == 0 ? "No new feed in that file." : "Imported " + added + (added == 1 ? " feed." : " feeds."));
      } catch (Exception e) {
        log("import: " + e);
        status = "That file is not a feed list.";
      }
      show(Screen.FEEDS);
    } else if (request == EXPORT) {
      // "wt" truncates: a shorter list over a longer file leaves no tail.
      try (OutputStream out = getContentResolver().openOutputStream(uri, "wt")) {
        feeds.write(out);
        status = "Exported " + feeds.list.size() + " feeds.";
      } catch (IOException e) {
        log("export: " + e);
        status = "The list could not be written there.";
      }
      show(Screen.FEEDS);
    }
  }

  // ---- Drawing ----
  // Flat black-and-white shapes with no ripple: an e-ink screen redraws
  // slowly, and an animation only leaves ghosts.

  private void setStatus(String s) {
    runOnUiThread(() -> {
      status = s;
      if (screen != Screen.ADD) show(screen);
    });
  }

  private void log(String line) {
    refresher.log(line);
  }

  private void hideKeyboard() {
    View v = getCurrentFocus();
    if (v != null) getSystemService(InputMethodManager.class).hideSoftInputFromWindow(v.getWindowToken(), 0);
  }

  private TextView text(String s, int sp, int color) {
    TextView t = new TextView(this);
    t.setText(s);
    t.setTextSize(sp);
    t.setTextColor(color);
    t.setLineSpacing(0, 1.15f);
    return t;
  }

  private TextView label(String s) {
    TextView t = text(s.toUpperCase(Locale.ROOT), 13, GREY);
    t.setLetterSpacing(0.14f);
    t.setTypeface(Typeface.DEFAULT_BOLD);
    return t;
  }

  private TextView link(String s, View.OnClickListener click) {
    TextView t = text(s, 16, INK);
    t.setPaintFlags(t.getPaintFlags() | Paint.UNDERLINE_TEXT_FLAG);
    t.setPadding(0, dp(10), 0, dp(10));
    if (click != null) t.setOnClickListener(click);
    return t;
  }

  // chip is one choice among others, filled when on.
  private TextView chip(String s, boolean on, View.OnClickListener click) {
    TextView c = text(s, 16, on ? PAPER : INK);
    c.setPadding(dp(16), dp(10), dp(16), dp(10));
    c.setBackground(box(on ? INK : PAPER, INK, 1, 20));
    c.setOnClickListener(click);
    return c;
  }

  private TextView button(String s, boolean primary, View.OnClickListener click) {
    TextView b = text(s, 18, primary ? PAPER : INK);
    b.setTypeface(Typeface.DEFAULT_BOLD);
    b.setGravity(Gravity.CENTER);
    b.setPadding(dp(24), dp(16), dp(24), dp(16));
    b.setBackground(box(primary ? INK : PAPER, INK, 2, 6));
    if (click != null) b.setOnClickListener(click);
    return b;
  }

  private EditText field(String hint, int action) {
    EditText e = new EditText(this);
    e.setHint(hint);
    e.setHintTextColor(GREY);
    e.setTextColor(INK);
    e.setTextSize(18);
    e.setSingleLine(true);
    e.setImeOptions(action);
    e.setPadding(dp(16), dp(14), dp(16), dp(14));
    e.setBackground(box(PAPER, INK, 2, 6));
    return e;
  }

  private GradientDrawable box(int fill, int stroke, int width, int radius) {
    GradientDrawable g = new GradientDrawable();
    g.setColor(fill);
    g.setStroke(dp(width), stroke);
    g.setCornerRadius(dp(radius));
    return g;
  }

  private LinearLayout column(int left, int top, int right, int bottom) {
    LinearLayout c = new LinearLayout(this);
    c.setOrientation(LinearLayout.VERTICAL);
    c.setPadding(left, top, right, bottom);
    return c;
  }

  private View rule(int color) {
    View v = new View(this);
    v.setBackgroundColor(color);
    return v;
  }

  private View space(int width) {
    View v = new View(this);
    v.setLayoutParams(new LinearLayout.LayoutParams(dp(width), 1));
    return v;
  }

  private LinearLayout.LayoutParams line(int top) {
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, dp(1));
    p.topMargin = dp(top);
    return p;
  }

  private LinearLayout.LayoutParams gap(int top) {
    LinearLayout.LayoutParams p = new LinearLayout.LayoutParams(-1, -2);
    p.topMargin = dp(top);
    return p;
  }

  private int dp(int v) {
    return Math.round(v * getResources().getDisplayMetrics().density);
  }
}
