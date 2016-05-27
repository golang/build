var mode = "all"

function readURL() {
	mode = window.location.hash.substr(1)
	if(mode.match(/\+muted/)) {
		$("#showmute").attr("checked","checked");
		mode = mode.replace(/\+muted/, "");
	}
	if(mode.match(/\+hidecl/)) {
		$("#showcl").attr("checked","");
		mode = mode.replace(/\+hidecl/, "");
	}
	if(mode.match(/\+hideissue/)) {
		$("#showissue").attr("checked","");
		mode = mode.replace(/\+hideissue/, "");
	}
}

function show(newmode) {
	mode = newmode
	redraw()
}

function redraw() {
	// Invariant: a tr containing a td with mine and todo classes itself has class todo.
	$("tr.todo").removeClass("todo");
	$("td.mine.todo").parent().addClass("todo");

	// Start with all items hidden.
	$("tr.item").addClass("hidden");
	$("tbody.dir").addClass("hidden");
	
	$("tr.item").removeClass("unhide");

	// Unhide the rows we want to show.
	var show;
	var showmute = $("#showmute").prop('checked');
	var showcl = $("#showcl").prop('checked');
	var showissue = $("#showissue").prop('checked');
	if(mode == "mine") {
		$("td.mine").parent().addClass("unhide");
	} else if(mode == "release") {
		$("td.release").parent().addClass("unhide");
	} else if(mode == "todo") {
		$("td.mine.todo").parent().addClass("unhide");
	} else if(mode == "unassigned") {
		var show = $("td.unassigned").parent();
		if(!showmute)
			show = show.not("tbody.muted tr.item");
		show.addClass("unhide");
	} else {
		mode = "all"
		if(showmute) {
			$("tr.item").addClass("unhide");
		} else {
			$("tbody:not(.muted) tr.item").addClass("unhide");
			$("td.mine").parent().addClass("unhide");
		}	
	}
	
	// But keep issues and/or CLs hidden.
	if(!showissue)
		$("td.issue").parent().removeClass("unhide");
	if(!showcl)
		$("td.codereview").parent().removeClass("unhide");

	$("tr.unhide").removeClass("hidden");	
	
	// Unhide the tbody containing the items we want to show.
	// Unhiding a tbody will unhide its directory row.
	$("tr.item:not(.hidden)").parent().removeClass("hidden");

	// Make the current mode look less like a link.
	$("a.showbar").removeClass("showing");
	$("#show-"+mode).addClass("showing");

	// Update window hash for bookmarking.
	var hash = mode
	if(showmute) {
		hash += "+muted"
	}
	if(!showcl) {
		hash += "+hidecl"
	}
	if(!showissue) {
		hash += "+hideissue"
	}
	window.location.hash = hash
}

function mute(ev, dir) {
	var dirclass = "dir-" + dir.replace(/\//g, "\\/").replace(/\./g, "\\.");
	
	var outer = $(ev.delegateTarget);
	var muting = outer.text() == "mute";
	var op = "";
	if(muting) {
		outer.text("muting...");
		op = "mute";
	} else {
		outer.text("unmuting...");
		op = "unmute";
	}
	console.log("Mute: " + dir)
	$.ajax({
		"type": "POST",
		"url": "/uiop",
		"data": {
			"dir": dir,
			"op": op
		},
		"success": function() {
			if(op == "mute") {
				$("tbody." + dirclass).addClass("muted");
				outer.text("unmute");
			} else {
				$("tbody." + dirclass).removeClass("muted");
				outer.text("mute");
			}
			redraw();
		},
		"error": function(xhr, status) {
			outer.text("failed: " + status)	
		}	
	})
}

function setreviewer(a, rev) {
	var clnumber = a.attr("id").replace("assign-", "");
	var who = rev.text();
	$.ajax({
		"type": "POST",
		"url": "/uiop",
		"data": {
			"cl": clnumber,
			"reviewer": who,
			"op": "reviewer"
		},
		"dataType": "text",
		"success": function(data) {
			a.text("edit");
			if(data.match(/^ERROR/)) {
				$("#err-" + clnumber).text(data);
				return;
			}
			rev.text(data);
		},
		"error": function(xhr, status) {
			a.text("failed: " + status)	
		}	
	})
}

$(document).ready(function() {
	// Define handler for mute links.
	$("a.mute").click(function(ev) {
		ev.preventDefault();
		var classes = $(ev.delegateTarget).attr("class").split(/\s+/);
		for(var i in classes) {
			var cl = classes[i];
			if(cl.substr(0,4) == "dir-") {
				mute(ev, cl.substr(4))
			}
		}
	})
	
	// Define handler for edit-reviewer links.
	$("a.assignreviewer").click(function(ev) {
		ev.preventDefault();
		var a = $(ev.delegateTarget);
		var revid = a.attr("id").replace("assign-", "reviewer-");
		var rev = $("#" + revid);
		if(a.text() == "edit") {
			rev.attr("contenteditable", "true");
			rev.focus();
			a.addClass("big");
			a.text("save");
		} else if(a.text() == "save") {
			a.text("saving...");
			a.removeClass("big");
			rev.attr("contenteditable", "false");
			setreviewer(a, rev);
		}
	})

	// Update mode from URL in browser and redraw.
	readURL();
	redraw();

	// Redraw any time the muting checkbox changes.
	$("#showmute").change(redraw);
	$("#showcl").change(redraw);
	$("#showissue").change(redraw);
})
